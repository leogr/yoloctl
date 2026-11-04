#!/usr/bin/env python3
"""Tag-only draft publication. Never rebuild, overwrite, retry, or publish a draft."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import urllib.error
import urllib.parse
import urllib.request
import zipfile


VERSION = re.compile(r'^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')


def identity(env):
    version = env['VERSION']
    if not VERSION.fullmatch(version) or len(version) > 128:
        raise ValueError('invalid strict release tag')
    if '-' in version:
        for part in version.split('-', 1)[1].split('.'):
            if part.isascii() and part.isdigit() and len(part) > 1 and part[0] == '0':
                raise ValueError('numeric prerelease has a leading zero')
    if env['GITHUB_EVENT_NAME'] != 'push' or env['GITHUB_REF'] != 'refs/tags/' + version:
        raise ValueError('only an exact pushed release tag can create a draft')
    if not re.fullmatch(r'[0-9a-f]{40}', env['SOURCE_SHA']):
        raise ValueError('invalid immutable source commit')
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', env['GITHUB_REPOSITORY']):
        raise ValueError('invalid repository')
    return version


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class GitHub:
    def __init__(self, env):
        self.env = env
        self.base = 'https://api.github.com/repos/' + env['GITHUB_REPOSITORY']
        self.headers = {'Authorization': 'Bearer ' + env['GH_TOKEN'],
                        'Accept': 'application/vnd.github+json',
                        'X-GitHub-Api-Version': '2022-11-28'}

    def request(self, path, data=None):
        body = None if data is None else json.dumps(data).encode()
        headers = dict(self.headers)
        if body is not None:
            headers['Content-Type'] = 'application/json'
        req = urllib.request.Request(self.base + path, data=body, headers=headers)
        with urllib.request.urlopen(req, timeout=60) as response:
            return json.load(response)

    def no_release(self, version):
        # Listing with this job's push access includes drafts. The tag lookup
        # endpoint alone is insufficient evidence that no draft exists.
        for page in range(1, 101):
            releases = self.request(f'/releases?per_page=100&page={page}')
            if not isinstance(releases, list):
                raise ValueError('release inventory is unavailable')
            if any(item['tag_name'] == version for item in releases):
                raise ValueError('a release already exists for this tag; never overwrite or resume it')
            if len(releases) < 100:
                return
        raise ValueError('release inventory exceeded its bound; nothing created')

    def exact_tag(self, version, commit):
        obj = self.request('/git/ref/tags/' + version)['object']
        for _ in range(8):
            if obj['type'] == 'commit':
                if obj['sha'] != commit:
                    raise ValueError('tag changed after the gated build')
                return
            if obj['type'] != 'tag':
                break
            obj = self.request('/git/tags/' + obj['sha'])['object']
        raise ValueError('tag does not resolve to the gated commit')

    def artifact_zip(self, artifact_id, destination):
        # Do not carry API credentials onto a signed storage redirect.
        req = urllib.request.Request(self.base + '/actions/artifacts/' + artifact_id + '/zip', headers=self.headers)
        opener = urllib.request.build_opener(NoRedirect)
        try:
            response = opener.open(req, timeout=60)
        except urllib.error.HTTPError as error:
            if error.code != 302:
                raise
            location = error.headers['Location']
            parsed = urllib.parse.urlsplit(location)
            if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password:
                raise ValueError('invalid artifact storage redirect') from None
            response = urllib.request.urlopen(location, timeout=60)
        with response, destination.open('xb') as output:
            total = 0
            while chunk := response.read(1 << 20):
                total += len(chunk)
                if total > 2 << 30:
                    raise ValueError('artifact is too large')
                output.write(chunk)
            output.flush()
            os.fsync(output.fileno())

    def upload(self, url, path):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != 'https' or parsed.hostname != 'uploads.github.com':
            raise ValueError('invalid release upload endpoint')
        headers = dict(self.headers, **{'Content-Type': 'application/octet-stream'})
        request = urllib.request.Request(url + '?name=' + urllib.parse.quote(path.name),
                                         data=path.read_bytes(), headers=headers)
        with urllib.request.urlopen(request, timeout=180) as response:
            return json.load(response)


def verify_artifact(api, env, version, archive, output):
    artifact_id = env['ARTIFACT_ID']
    if not artifact_id.isascii() or not artifact_id.isdigit():
        raise ValueError('invalid gated artifact ID')
    digest = env['ARTIFACT_DIGEST']
    if not re.fullmatch('[0-9a-f]{64}', digest):
        raise ValueError('invalid gated artifact digest')
    meta = api.request('/actions/artifacts/' + artifact_id)
    if (meta['name'] != 'release-bundles' or meta['expired']
            or str(meta['workflow_run']['id']) != env['GITHUB_RUN_ID']
            or meta['digest'] != 'sha256:' + digest):
        raise ValueError('artifact does not belong to the gated run and digest')
    api.artifact_zip(artifact_id, archive)
    with archive.open('rb') as data:
        if hashlib.file_digest(data, 'sha256').hexdigest() != digest:
            raise ValueError('downloaded artifact digest mismatch')
    expected = {'SHA256SUMS'} | {f'yoloctl-{osname}-{arch}.tar.gz'
                               for osname in ('linux', 'darwin') for arch in ('amd64', 'arm64')}
    output.mkdir(mode=0o700)
    with zipfile.ZipFile(archive) as zipped:
        names = zipped.namelist()
        if set(names) != expected or len(names) != 5:
            raise ValueError('unexpected gated artifact entries')
        for entry in zipped.infolist():
            if entry.file_size > 512 << 20:
                raise ValueError('oversized artifact entry')
            with zipped.open(entry) as source, (output / entry.filename).open('xb') as target:
                while chunk := source.read(1 << 20):
                    target.write(chunk)
                target.flush()
                os.fsync(target.fileno())


def verify_final_bytes(env, version, output):
    archive = Path(env['RUNNER_TEMP']) / 'release-artifact.zip'
    with archive.open('rb') as data:
        if hashlib.file_digest(data, 'sha256').hexdigest() != env['ARTIFACT_DIGEST']:
            raise ValueError('gated artifact changed before upload')
    expected = {'SHA256SUMS'} | {f'yoloctl-{osname}-{arch}.tar.gz'
                               for osname in ('linux', 'darwin') for arch in ('amd64', 'arm64')}
    if {p.name for p in output.iterdir()} != expected:
        raise ValueError('final upload inventory changed')
    with zipfile.ZipFile(archive) as zipped:
        if set(zipped.namelist()) != expected or len(zipped.infolist()) != 5:
            raise ValueError('gated artifact entries changed')
        for name in expected:
            path = output / name
            if path.is_symlink() or not path.is_file():
                raise ValueError('final asset is not a regular file')
            with zipped.open(name) as original, path.open('rb') as final:
                if hashlib.file_digest(original, 'sha256').digest() != hashlib.file_digest(final, 'sha256').digest():
                    raise ValueError('final bytes differ from the gated artifact')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('phase', choices=('verify', 'draft'))
    parser.add_argument('--dist', type=Path, required=True)
    args = parser.parse_args()
    env = os.environ
    version = identity(env)
    api = GitHub(env)
    if args.phase == 'draft':
        verify_final_bytes(env, version, args.dist)
    api.exact_tag(version, env['SOURCE_SHA'])
    api.no_release(version)
    if args.phase == 'verify':
        verify_artifact(api, env, version, Path(env['RUNNER_TEMP']) / 'release-artifact.zip', args.dist)
        return
    release = api.request('/releases', {'tag_name': version, 'target_commitish': env['SOURCE_SHA'],
                                      'name': version, 'draft': True, 'prerelease': '-' in version,
                                      'body': 'Verify SHA256SUMS and GitHub attestations before installing.\n'
                                              'This draft requires maintainer publication.'})
    if not release['draft']:
        raise ValueError('server did not create a draft; no assets uploaded')
    url = release['upload_url'].split('{', 1)[0]
    for path in sorted(args.dist.iterdir()):
        api.upload(url, path)
    print('Draft created:', release['html_url'])


if __name__ == '__main__':
    main()

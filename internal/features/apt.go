package features

// Keep signed metadata and deb downloads across installer refreshes, outside
// image layers. Apt names list files by repository/suite/architecture; the
// shared lock protects updates and package reads from concurrent builders.
const aptCacheMounts = `--mount=type=cache,id=yoloctl-apt-archives-v1,target=/var/cache/yoloctl/apt,sharing=locked \
    --mount=type=cache,id=yoloctl-apt-lists-v1,target=/var/lib/apt/lists,sharing=locked`

func aptInstallDockerfile(packages string) string {
	return "RUN " + aptCacheMounts + ` \
    apt-get -o Acquire::Retries=3 update --error-on=any \
    && apt-get -o Acquire::Retries=3 -o Dir::Cache::Archives=/var/cache/yoloctl/apt \
        -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends ` + packages + "\n"
}

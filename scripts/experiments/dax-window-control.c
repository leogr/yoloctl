/*
 * Native Linux control for the guarded-mapping design hypothesis.
 * This is NOT a KVM/virtiofs test or a concurrency/EOF-semantics proof.
 * Uses one fresh, immediately unlinked /tmp file and at most four pages.
 * Compile: cc -std=c11 -O2 -Wall -Wextra -Werror dax-window-control.c -o /tmp/dax-window-control
 */
#define _GNU_SOURCE
#include <errno.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/resource.h>
#include <sys/statfs.h>
#include <sys/types.h>
#include <sys/utsname.h>
#include <sys/wait.h>
#include <unistd.h>

#define REQUIRE(test) do { \
    if (!(test)) { \
        fprintf(stderr, "control failed at line %d: %s (errno=%d)\n", \
                __LINE__, #test, errno); \
        exit(1); \
    } \
} while (0)

static unsigned char *file_map(int fd, size_t length, void *address)
{
    int flags = MAP_SHARED | (address ? MAP_FIXED : 0);
    void *result = mmap(address, length, PROT_READ | PROT_WRITE, flags, fd, 0);
    REQUIRE(result != MAP_FAILED);
    if (address)
        REQUIRE(result == address);
    return result;
}

static void guard_tail(unsigned char *base, size_t length, size_t size, size_t page)
{
    size_t boundary = (size + page - 1) / page * page;
    REQUIRE(boundary <= length);
    if (boundary == length)
        return;
    void *address = base + boundary;
    void *result = mmap(address, length - boundary, PROT_READ | PROT_WRITE,
                        MAP_PRIVATE | MAP_ANONYMOUS | MAP_FIXED, -1, 0);
    REQUIRE(result == address);
}

int main(void)
{
    alarm(10);
    struct rlimit no_core = {0, 0};
    REQUIRE(setrlimit(RLIMIT_CORE, &no_core) == 0);
    long pages = sysconf(_SC_PAGESIZE);
    REQUIRE(pages > 0 && pages <= 65536);
    size_t page = (size_t)pages;
    size_t length = 4 * page;
    char path[] = "/tmp/yoloctl-dax-window-XXXXXX";
    int fd = mkstemp(path);
    REQUIRE(fd >= 0);
    REQUIRE(unlink(path) == 0);
    struct statfs fs;
    REQUIRE(fstatfs(fd, &fs) == 0);
    REQUIRE(fs.f_type != 0x01021997); /* V9FS_MAGIC: never exercise 9p. */
    REQUIRE(ftruncate(fd, (off_t)length) == 0);

    /* Negative control: ordinary file mappings fault beyond a later EOF. */
    pid_t child = fork();
    REQUIRE(child >= 0);
    if (child == 0) {
        alarm(3);
        volatile unsigned char *mapping = file_map(fd, length, NULL);
        REQUIRE(ftruncate(fd, 0) == 0);
        volatile unsigned char value = mapping[page];
        (void)value;
        _exit(2);
    }
    int status = 0;
    REQUIRE(waitpid(child, &status, 0) == child);
    REQUIRE(WIFSIGNALED(status) && WTERMSIG(status) == SIGBUS);
    REQUIRE(ftruncate(fd, (off_t)length) == 0);

    unsigned char *a = file_map(fd, length, NULL);
    unsigned char *b = file_map(fd, length, NULL);
    memset(a, 0x41, length);
    REQUIRE(b[0] == 0x41 && b[3 * page] == 0x41);

    /* Replace whole pages past the new EOF before touching the backing file. */
    size_t shortened = page + 17;
    guard_tail(a, length, shortened, page);
    guard_tail(b, length, shortened, page);
    REQUIRE(ftruncate(fd, (off_t)shortened) == 0);
    REQUIRE(a[0] == 0x41 && b[page + 16] == 0x41);
    REQUIRE(a[page + 17] == 0 && b[page + 17] == 0); /* partial EOF page */
    REQUIRE(a[2 * page] == 0 && b[3 * page] == 0);   /* anonymous guards */
    a[0] = 0x42;
    REQUIRE(b[0] == 0x42);
    a[2 * page] = 0x99; /* guard stores must never become durable file data */
    REQUIRE(b[2 * page] == 0);

    /* Grow first, populate the ordinary file, then restore the mappings. */
    REQUIRE(ftruncate(fd, (off_t)length) == 0);
    unsigned char marker = 0x43;
    REQUIRE(pwrite(fd, &marker, 1, (off_t)(2 * page)) == 1);
    file_map(fd, length, a);
    file_map(fd, length, b);
    REQUIRE(a[0] == 0x42 && b[0] == 0x42);
    REQUIRE(a[2 * page] == 0x43 && b[2 * page] == 0x43);
    REQUIRE(a[3 * page] == 0 && b[3 * page] == 0);
    a[3 * page] = 0x44;
    REQUIRE(b[3 * page] == 0x44);
    unsigned char stored = 0;
    REQUIRE(pread(fd, &stored, 1, (off_t)(3 * page)) == 1 && stored == 0x44);

    guard_tail(a, length, 0, page);
    guard_tail(b, length, 0, page);
    REQUIRE(ftruncate(fd, 0) == 0);
    REQUIRE(a[0] == 0 && b[3 * page] == 0);
    REQUIRE(munmap(a, length) == 0 && munmap(b, length) == 0);
    REQUIRE(close(fd) == 0);
    struct utsname system;
    REQUIRE(uname(&system) == 0);
    printf("{\"status\":\"passed\",\"scope\":\"native-mapping-control\","
           "\"kernel\":\"%s\",\"page_size\":%zu,\"filesystem_magic\":%ld,"
           "\"negative_control\":\"SIGBUS\","
           "\"limits\":\"No VM, cross-process coordinator, racing accesses, "
           "guest EOF, durability, host edits, or macOS validation.\"}\n",
           system.release, page, (long)fs.f_type);
    return 0;
}

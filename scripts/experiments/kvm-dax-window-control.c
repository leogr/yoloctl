/*
 * Linux/x86_64 host experiment: replace file-backed KVM memory while two
 * synthetic guests continuously read it. NOT a filesystem/virtiofs test.
 * No Lima, networking, guest OS, root privilege, or existing files are used.
 * An immediately unlinked /var/tmp file is the only backing store.
 * Compile with: cc -std=c11 -O2 -Wall -Wextra -Werror -pthread ... -o ...
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/kvm.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <sys/resource.h>
#include <sys/statfs.h>
#include <sys/utsname.h>
#include <time.h>
#include <unistd.h>

#if !defined(__linux__) || !defined(__x86_64__)
#error This experiment targets Linux x86_64 only.
#endif

#define PAGE 4096u
#define WINDOW (4u * PAGE)
#define RAM 0x10000u
#define CYCLES 64
#define REQUIRE(test) do { \
    if (!(test)) { \
        fprintf(stderr, "control failed at line %d: %s (errno=%d)\n", \
                __LINE__, #test, errno); \
        exit(1); \
    } \
} while (0)

struct vm {
    int fd, cpu;
    size_t run_size;
    struct kvm_run *run;
    unsigned char *ram, *window;
    pthread_t thread;
    atomic_bool stop;
    atomic_int error;
    atomic_uint cycles;
    atomic_uint words[4];
};

static void *map_file(int fd, void *at)
{
    void *result = mmap(at, WINDOW, PROT_READ | PROT_WRITE,
                        MAP_SHARED | (at ? MAP_FIXED : 0), fd, 0);
    REQUIRE(result != MAP_FAILED);
    REQUIRE(!at || result == at);
    return result;
}

static void guard_tail(struct vm *vm, size_t new_size)
{
    size_t boundary = (new_size + PAGE - 1) / PAGE * PAGE;
    REQUIRE(boundary < WINDOW);
    void *at = vm->window + boundary;
    REQUIRE(mmap(at, WINDOW - boundary, PROT_READ | PROT_WRITE,
                 MAP_PRIVATE | MAP_ANONYMOUS | MAP_FIXED, -1, 0) == at);
}

static void *run_cpu(void *arg)
{
    struct vm *vm = arg;
    while (!atomic_load(&vm->stop)) {
        if (ioctl(vm->cpu, KVM_RUN, 0) < 0) {
            if (errno == EINTR)
                continue;
            atomic_store(&vm->error, errno);
            return NULL;
        }
        struct kvm_run *r = vm->run;
        if (r->exit_reason != KVM_EXIT_IO || r->io.direction != KVM_EXIT_IO_OUT ||
            r->io.size != 2 || r->io.count != 1 ||
            r->io.port < 0x10 || r->io.port > 0x13 ||
            r->io.data_offset > vm->run_size - 2) {
            fprintf(stderr, "unexpected guest exit: reason=%u\n", r->exit_reason);
            atomic_store(&vm->error, EPROTO);
            return NULL;
        }
        uint16_t value;
        memcpy(&value, (unsigned char *)r + r->io.data_offset, sizeof(value));
        unsigned int page = r->io.port - 0x10;
        atomic_store(&vm->words[page], value);
        if (page == 3)
            atomic_fetch_add(&vm->cycles, 1);
    }
    return NULL;
}

static void setup(struct vm *vm, int kvm, int file)
{
    vm->fd = ioctl(kvm, KVM_CREATE_VM, 0);
    REQUIRE(vm->fd >= 0);
    /* Dedicated internal addresses, disjoint from our guest memory slots. */
    REQUIRE(ioctl(vm->fd, KVM_SET_TSS_ADDR, 0xfffbd000ul) == 0);
    vm->ram = mmap(NULL, RAM, PROT_READ | PROT_WRITE,
                   MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    REQUIRE(vm->ram != MAP_FAILED);
    vm->window = map_file(file, NULL);
    struct kvm_userspace_memory_region region = {
        .slot = 0, .guest_phys_addr = 0, .memory_size = RAM,
        .userspace_addr = (uintptr_t)vm->ram,
    };
    REQUIRE(ioctl(vm->fd, KVM_SET_USER_MEMORY_REGION, &region) == 0);
    region.slot = 1;
    region.guest_phys_addr = RAM;
    region.memory_size = WINDOW;
    region.userspace_addr = (uintptr_t)vm->window;
    REQUIRE(ioctl(vm->fd, KVM_SET_USER_MEMORY_REGION, &region) == 0);

    vm->cpu = ioctl(vm->fd, KVM_CREATE_VCPU, 0);
    REQUIRE(vm->cpu >= 0);
    int run_size = ioctl(kvm, KVM_GET_VCPU_MMAP_SIZE, 0);
    REQUIRE(run_size >= (int)sizeof(struct kvm_run));
    vm->run_size = (size_t)run_size;
    vm->run = mmap(NULL, vm->run_size, PROT_READ | PROT_WRITE, MAP_SHARED, vm->cpu, 0);
    REQUIRE(vm->run != MAP_FAILED);
    struct kvm_sregs s;
    REQUIRE(ioctl(vm->cpu, KVM_GET_SREGS, &s) == 0);
    s.cs = (struct kvm_segment){.base = 0, .limit = 0xffff, .type = 11, .present = 1, .s = 1};
    s.ds = (struct kvm_segment){.base = RAM, .limit = 0xffff, .selector = RAM >> 4,
                               .type = 3, .present = 1, .s = 1};
    s.ss = (struct kvm_segment){.base = 0, .limit = 0xffff, .type = 3, .present = 1, .s = 1};
    REQUIRE(ioctl(vm->cpu, KVM_SET_SREGS, &s) == 0);
    struct kvm_regs regs = {.rip = 0x1000, .rsp = RAM - 16, .rflags = 2};
    REQUIRE(ioctl(vm->cpu, KVM_SET_REGS, &regs) == 0);
    /* 16-bit real mode: read one word per shared page, report via OUT, repeat.
     * Guest code never executes from or writes to the file-backed window.
     */
    static const unsigned char code[] = {
        0xba, 0x10, 0x00,       /* mov dx, 0x10 */
        0xa1, 0x00, 0x00, 0xef, /* mov ax, [0x0000]; out dx, ax */
        0x42,                  /* inc dx */
        0xa1, 0x00, 0x10, 0xef, /* mov ax, [0x1000]; out dx, ax */
        0x42,
        0xa1, 0x00, 0x20, 0xef,
        0x42,
        0xa1, 0x00, 0x30, 0xef,
        0xeb, 0xe8,            /* jmp back 24 bytes to mov dx */
    };
    memcpy(vm->ram + regs.rip, code, sizeof(code));
    atomic_init(&vm->stop, 0);
    atomic_init(&vm->error, 0);
    atomic_init(&vm->cycles, 0);
    for (int i = 0; i < 4; i++)
        atomic_init(&vm->words[i], 0);
    REQUIRE(pthread_create(&vm->thread, NULL, run_cpu, vm) == 0);
}

static void observe(struct vm *vm, const uint16_t expected[4])
{
    unsigned int before = atomic_load(&vm->cycles);
    /* Two full report cycles ensure no pre-transition partial cycle is used. */
    for (int retry = 0; retry < 1000; retry++) {
        REQUIRE(atomic_load(&vm->error) == 0);
        if (atomic_load(&vm->cycles) >= before + 2) {
            for (int i = 0; i < 4; i++)
                REQUIRE(atomic_load(&vm->words[i]) == expected[i]);
            return;
        }
        usleep(1000);
    }
    fprintf(stderr, "guest made no progress within the observation deadline\n");
    exit(1);
}

int main(void)
{
    alarm(20);
    struct rlimit no_core = {0, 0};
    REQUIRE(setrlimit(RLIMIT_CORE, &no_core) == 0);
    REQUIRE(sysconf(_SC_PAGESIZE) == PAGE);
    int kvm = open("/dev/kvm", O_RDWR | O_CLOEXEC);
    if (kvm < 0) {
        fprintf(stderr, "No accessible /dev/kvm; run on the Linux x86_64 host (errno=%d).\n", errno);
        return 77;
    }
    REQUIRE(ioctl(kvm, KVM_GET_API_VERSION, 0) == KVM_API_VERSION);
    REQUIRE(ioctl(kvm, KVM_CHECK_EXTENSION, KVM_CAP_USER_MEMORY) > 0);
    char path[] = "/var/tmp/yoloctl-kvm-window-XXXXXX";
    int file = mkstemp(path);
    REQUIRE(file >= 0);
    REQUIRE(unlink(path) == 0);
    struct statfs fs;
    REQUIRE(fstatfs(file, &fs) == 0);
    REQUIRE(fs.f_type != 0x01021997); /* Never run this on 9p. */
    REQUIRE(ftruncate(file, WINDOW) == 0);
    struct vm vm[2] = {0};
    setup(&vm[0], kvm, file);
    setup(&vm[1], kvm, file);
    uint16_t zero[4] = {0};
    for (int i = 0; i < 2; i++)
        observe(&vm[i], zero);

    for (int cycle = 0; cycle < CYCLES; cycle++) {
        uint16_t full[4], partial[4];
        /* Populate the real file before restoring its mappings in both VMs. */
        REQUIRE(ftruncate(file, WINDOW) == 0);
        for (int i = 0; i < 4; i++) {
            full[i] = (uint16_t)(100 + cycle * 4 + i);
            partial[i] = i < 2 ? full[i] : 0;
            REQUIRE(pwrite(file, &full[i], sizeof(full[i]), i * PAGE) == sizeof(full[i]));
        }
        for (int i = 0; i < 2; i++) {
            map_file(file, vm[i].window);
            observe(&vm[i], full);
        }
        /* Guest vCPUs keep running across every MAP_FIXED and ftruncate. */
        for (int i = 0; i < 2; i++)
            guard_tail(&vm[i], PAGE + 17);
        REQUIRE(ftruncate(file, PAGE + 17) == 0);
        for (int i = 0; i < 2; i++)
            observe(&vm[i], partial);
        for (int i = 0; i < 2; i++)
            guard_tail(&vm[i], 0);
        REQUIRE(ftruncate(file, 0) == 0);
        for (int i = 0; i < 2; i++)
            observe(&vm[i], zero);
    }
    for (int i = 0; i < 2; i++) {
        atomic_store(&vm[i].stop, 1);
        REQUIRE(pthread_join(vm[i].thread, NULL) == 0);
        REQUIRE(atomic_load(&vm[i].error) == 0);
        REQUIRE(munmap(vm[i].run, vm[i].run_size) == 0);
        REQUIRE(close(vm[i].cpu) == 0 && close(vm[i].fd) == 0);
        REQUIRE(munmap(vm[i].window, WINDOW) == 0);
        REQUIRE(munmap(vm[i].ram, RAM) == 0);
    }
    REQUIRE(close(file) == 0 && close(kvm) == 0);
    struct utsname host;
    REQUIRE(uname(&host) == 0);
    printf("{\"status\":\"passed\",\"scope\":\"two-bare-kvm-guests\","
           "\"kernel\":\"%s\",\"filesystem_magic\":%ld,\"cycles\":%d,"
           "\"limits\":\"One VMM process, read-only guest workload; no Linux guest, "
           "virtiofs, metadata/locks, guest-write durability, external host writers, "
           "fault recovery, or macOS qualification.\"}\n",
           host.release, (long)fs.f_type, CYCLES);
    return 0;
}

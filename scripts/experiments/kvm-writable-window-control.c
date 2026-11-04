/* Bounded synthetic Linux/x86_64 mapping experiment, not a filesystem.
 * Two separate VMM processes write/read one unlinked file. The coordinator
 * guards mappings before shrink and fences one deliberately stopped worker.
 * --native-control exercises the same coordinator with native worker threads.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <linux/kvm.h>
#include <poll.h>
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
#include <sys/stat.h>
#include <sys/statfs.h>
#include <sys/utsname.h>
#include <sys/wait.h>
#include <unistd.h>

#if !defined(__linux__) || !defined(__x86_64__)
#error This experiment targets Linux x86_64 only.
#endif

#define PAGE 4096u
#define WINDOW (4u * PAGE)
#define RAM 0x10000u
#define MAILBOX 0x800u
#define CYCLES 64
#define REQUIRE(test) do { if (!(test)) { \
    fprintf(stderr, "control failed: line %d: %s (errno=%d)\n", \
            __LINE__, #test, errno); exit(1); } } while (0)

enum operation { OP_CONFIGURE, OP_MAP_FILE, OP_GUARD, OP_OBSERVE, OP_STOP };
struct request {
    uint32_t sequence, operation, argument;
    uint16_t expected[8];
};
struct reply { uint32_t sequence, cycles; };
struct client { pid_t pid; int send, receive; uint32_t sequence, cycles; };
static struct client clients[2];

struct vm {
    int fd, cpu, index;
    size_t run_size;
    struct kvm_run *run;
    unsigned char *ram, *window;
    pthread_t thread;
    atomic_bool stop;
    atomic_int error;
    atomic_uint cycles, words[8];
};

static void deadline(int sig)
{
    (void)sig;
    for (int i = 0; i < 2; i++)
        if (clients[i].pid > 0)
            kill(clients[i].pid, SIGKILL);
    _exit(124);
}

static void cleanup(void)
{
    for (int i = 0; i < 2; i++) {
        if (clients[i].pid <= 0)
            continue;
        kill(clients[i].pid, SIGKILL);
        while (waitpid(clients[i].pid, NULL, 0) < 0 && errno == EINTR) {}
        clients[i].pid = 0;
    }
}

static void transfer(int fd, void *data, size_t length, int writing)
{
    unsigned char *p = data;
    while (length) {
        ssize_t n = writing ? write(fd, p, length) : read(fd, p, length);
        if (n < 0 && errno == EINTR)
            continue;
        REQUIRE(n > 0);
        p += n;
        length -= (size_t)n;
    }
}

static void *map_file(int fd, void *at)
{
    void *p = mmap(at, WINDOW, PROT_READ | PROT_WRITE,
                   MAP_SHARED | (at ? MAP_FIXED : 0), fd, 0);
    REQUIRE(p != MAP_FAILED && (!at || p == at));
    return p;
}

static void guard(struct vm *vm, size_t size)
{
    size_t boundary = (size + PAGE - 1) / PAGE * PAGE;
    REQUIRE(boundary < WINDOW);
    void *at = vm->window + boundary;
    REQUIRE(mmap(at, WINDOW - boundary, PROT_READ | PROT_WRITE,
                 MAP_PRIVATE | MAP_ANONYMOUS | MAP_FIXED, -1, 0) == at);
}

static void *native_cpu(void *arg)
{
    struct vm *vm = arg;
    while (!atomic_load(&vm->stop)) {
        for (unsigned int page = 0; page < 4; page++) {
            volatile uint16_t *words = (void *)(vm->window + page * PAGE);
            uint16_t value = atomic_load((_Atomic uint16_t *)(vm->ram + MAILBOX));
            words[1 + vm->index] = value + page;
            atomic_store(&vm->words[page * 2], words[0]);
            atomic_store(&vm->words[page * 2 + 1], words[2 - vm->index]);
        }
        atomic_fetch_add(&vm->cycles, 1);
    }
    return NULL;
}

static void *kvm_cpu(void *arg)
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
            r->io.size != 2 || r->io.count != 1 || r->io.port < 0x10 ||
            r->io.port > 0x17 || r->io.data_offset > vm->run_size - 2) {
            fprintf(stderr, "unexpected guest exit: reason=%u\n", r->exit_reason);
            atomic_store(&vm->error, EPROTO);
            return NULL;
        }
        uint16_t value;
        memcpy(&value, (unsigned char *)r + r->io.data_offset, sizeof(value));
        unsigned int word = r->io.port - 0x10;
        atomic_store(&vm->words[word], value);
        if (word == 7)
            atomic_fetch_add(&vm->cycles, 1);
    }
    return NULL;
}

static void setup(struct vm *vm, int file, int native)
{
    vm->ram = mmap(NULL, RAM, PROT_READ | PROT_WRITE,
                   MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
    REQUIRE(vm->ram != MAP_FAILED);
    vm->window = map_file(file, NULL);
    atomic_init((_Atomic uint16_t *)(vm->ram + MAILBOX), 0);
    atomic_init(&vm->stop, 0);
    atomic_init(&vm->error, 0);
    atomic_init(&vm->cycles, 0);
    for (int i = 0; i < 8; i++)
        atomic_init(&vm->words[i], 0);
    if (!native) {
        int kvm = open("/dev/kvm", O_RDWR | O_CLOEXEC);
        REQUIRE(kvm >= 0 && ioctl(kvm, KVM_GET_API_VERSION, 0) == KVM_API_VERSION);
        REQUIRE(ioctl(kvm, KVM_CHECK_EXTENSION, KVM_CAP_USER_MEMORY) > 0);
        vm->fd = ioctl(kvm, KVM_CREATE_VM, 0);
        REQUIRE(vm->fd >= 0);
        REQUIRE(ioctl(vm->fd, KVM_SET_TSS_ADDR, 0xfffbd000ul) == 0);
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
        int size = ioctl(kvm, KVM_GET_VCPU_MMAP_SIZE, 0);
        REQUIRE(size >= (int)sizeof(struct kvm_run));
        vm->run_size = (size_t)size;
        vm->run = mmap(NULL, vm->run_size, PROT_READ | PROT_WRITE, MAP_SHARED, vm->cpu, 0);
        REQUIRE(vm->run != MAP_FAILED);
        struct kvm_sregs s;
        REQUIRE(ioctl(vm->cpu, KVM_GET_SREGS, &s) == 0);
        s.cs = (struct kvm_segment){.limit = 0xffff, .type = 11, .present = 1, .s = 1};
        s.ds = (struct kvm_segment){.base = RAM, .limit = 0xffff, .selector = RAM >> 4,
                                   .type = 3, .present = 1, .s = 1};
        s.ss = (struct kvm_segment){.limit = 0xffff, .type = 3, .present = 1, .s = 1};
        REQUIRE(ioctl(vm->cpu, KVM_SET_SREGS, &s) == 0);
        struct kvm_regs regs = {.rip = 0x1000, .rsp = RAM - 16, .rflags = 2};
        REQUIRE(ioctl(vm->cpu, KVM_SET_REGS, &regs) == 0);
        /* Real-mode code writes a mailbox-selected word into its own slot on
         * every page, then reports the host word and the peer's slot via OUT.
         * Code/mailbox RAM is never replaced. No guest stack or interrupts.
         */
        unsigned char *code = vm->ram + regs.rip;
        size_t n = 0;
        code[n++] = 0xba; code[n++] = 0x10; code[n++] = 0; /* mov dx, 0x10 */
        for (unsigned int page = 0; page < 4; page++) {
            unsigned int own = page * PAGE + 2u * (1u + (unsigned int)vm->index);
            unsigned int peer = page * PAGE + 2u * (2u - (unsigned int)vm->index);
            unsigned char block[] = {
                0x2e, 0xa1, 0x00, 0x08,       /* mov ax, cs:[mailbox] */
                0x05, (unsigned char)page, 0,  /* add ax, page */
                0xa3, own & 255, own >> 8,     /* mov [own], ax */
                0xa1, 0, (page * PAGE) >> 8, 0xef, 0x42,
                0xa1, peer & 255, peer >> 8, 0xef, 0x42,
            };
            memcpy(code + n, block, sizeof(block));
            n += sizeof(block);
        }
        int displacement = -(int)(n + 3);
        code[n++] = 0xe9;                    /* near relative jump */
        code[n++] = (unsigned int)displacement & 255;
        code[n++] = ((unsigned int)displacement >> 8) & 255;
        REQUIRE(n < RAM - regs.rip);
        REQUIRE(close(kvm) == 0);
    }
    REQUIRE(pthread_create(&vm->thread, NULL, native ? native_cpu : kvm_cpu, vm) == 0);
}

static void observe(struct vm *vm, const uint16_t expected[8])
{
    unsigned int before = atomic_load(&vm->cycles);
    for (int retry = 0; retry < 1000; retry++) {
        REQUIRE(atomic_load(&vm->error) == 0);
        if (atomic_load(&vm->cycles) >= before + 2) {
            for (int i = 0; i < 8; i++) {
                unsigned int got = atomic_load(&vm->words[i]);
                if (got != expected[i]) {
                    fprintf(stderr, "worker %d word %d: got %u expected %u\n",
                            vm->index, i, got, expected[i]);
                    exit(1);
                }
            }
            return;
        }
        usleep(1000);
    }
    fprintf(stderr, "worker %d made no progress\n", vm->index);
    exit(1);
}

static void worker(int index, int input, int output, int file, int native)
{
    /* No parent KVM objects or threads exist when fork occurs. */
    struct vm vm = {.index = index};
    setup(&vm, file, native);
    uint32_t sequence = 0;
    for (;;) {
        struct request req;
        transfer(input, &req, sizeof(req), 0);
        REQUIRE(req.sequence == ++sequence);
        switch (req.operation) {
        case OP_CONFIGURE:
            atomic_store((_Atomic uint16_t *)(vm.ram + MAILBOX), (uint16_t)req.argument);
            break;
        case OP_MAP_FILE: map_file(file, vm.window); break;
        case OP_GUARD: guard(&vm, req.argument); break;
        case OP_OBSERVE: observe(&vm, req.expected); break;
        case OP_STOP:
            atomic_store(&vm.stop, 1);
            REQUIRE(pthread_join(vm.thread, NULL) == 0);
            REQUIRE(atomic_load(&vm.error) == 0);
            break;
        default: REQUIRE(0);
        }
        struct reply reply = {req.sequence, atomic_load(&vm.cycles)};
        transfer(output, &reply, sizeof(reply), 1);
        if (req.operation == OP_STOP)
            _exit(0); /* Kernel closes this worker's mappings and KVM handles. */
    }
}

static void send_request(int index, struct request req)
{
    req.sequence = ++clients[index].sequence;
    transfer(clients[index].send, &req, sizeof(req), 1);
}

static int receive_reply(int index, int milliseconds)
{
    struct pollfd fd = {.fd = clients[index].receive, .events = POLLIN};
    int result;
    do { result = poll(&fd, 1, milliseconds); } while (result < 0 && errno == EINTR);
    REQUIRE(result >= 0);
    if (!result)
        return 0;
    REQUIRE(fd.revents & POLLIN);
    struct reply reply;
    transfer(fd.fd, &reply, sizeof(reply), 0);
    REQUIRE(reply.sequence == clients[index].sequence);
    clients[index].cycles = reply.cycles;
    return 1;
}

static void rpc(int index, struct request req)
{
    send_request(index, req);
    REQUIRE(receive_reply(index, 2000));
}

static uint16_t pattern(int index, int cycle)
{
    return (uint16_t)(0x1000 + index * 0x2000 + cycle * 4);
}

static void sample(size_t size, int cycle)
{
    for (int index = 0; index < 2; index++) {
        if (clients[index].pid <= 0)
            continue;
        struct request req = {.operation = OP_OBSERVE};
        for (unsigned int page = 0; page < 4; page++) {
            if (page * PAGE >= size)
                continue;
            req.expected[page * 2] = (uint16_t)(100 + cycle * 4 + page);
            req.expected[page * 2 + 1] = pattern(1 - index, cycle) + page;
        }
        rpc(index, req);
    }
}

static void check_file(int file, size_t size, int cycle, int populated)
{
    struct stat st;
    REQUIRE(fstat(file, &st) == 0 && st.st_size == (off_t)size);
    unsigned char got[WINDOW], expected[WINDOW] = {0};
    REQUIRE(pread(file, got, sizeof(got), 0) == (ssize_t)size);
    for (unsigned int page = 0; page * PAGE < size; page++) {
        uint16_t words[] = {(uint16_t)(100 + cycle * 4 + page),
                           populated ? pattern(0, cycle) + page : 0,
                           populated ? pattern(1, cycle) + page : 0};
        memcpy(expected + page * PAGE, words, sizeof(words));
    }
    REQUIRE(memcmp(got, expected, size) == 0);
}

static void restore(int file, int cycle, int initially_mapped)
{
    REQUIRE(ftruncate(file, WINDOW) == 0);
    for (unsigned int page = 0; page < 4; page++) {
        uint16_t value = (uint16_t)(100 + cycle * 4 + page);
        REQUIRE(pwrite(file, &value, sizeof(value), page * PAGE) == sizeof(value));
    }
    /* After zero-length guarding, guest writes into anonymous pages must not
     * reappear in the newly extended host file. Initial mappings are active. */
    if (!initially_mapped)
        check_file(file, WINDOW, cycle, 0);
    for (int i = 0; i < 2; i++)
        rpc(i, (struct request){.operation = OP_CONFIGURE, .argument = pattern(i, cycle)});
    for (int i = 0; i < 2; i++)
        rpc(i, (struct request){.operation = OP_MAP_FILE});
    sample(WINDOW, cycle);
    check_file(file, WINDOW, cycle, 1);
    REQUIRE(fsync(file) == 0); /* Successful syscall only; no crash-durability claim. */
}

static void shrink(int file, size_t size, int cycle)
{
    for (int i = 0; i < 2; i++)
        if (clients[i].pid > 0)
            rpc(i, (struct request){.operation = OP_GUARD, .argument = (uint32_t)size});
    REQUIRE(ftruncate(file, (off_t)size) == 0);
    sample(size, cycle);
    check_file(file, size, cycle, 1);
}

int main(int argc, char **argv)
{
    REQUIRE(argc == 3 && (!strcmp(argv[1], "--kvm") || !strcmp(argv[1], "--native-control")));
    int native = !strcmp(argv[1], "--native-control");
    REQUIRE(sysconf(_SC_PAGESIZE) == PAGE);
    struct rlimit core = {0, 0};
    REQUIRE(setrlimit(RLIMIT_CORE, &core) == 0);
    REQUIRE(signal(SIGPIPE, SIG_IGN) != SIG_ERR);
    REQUIRE(signal(SIGALRM, deadline) != SIG_ERR);
    REQUIRE(atexit(cleanup) == 0);
    alarm(20);
    if (!native && access("/dev/kvm", R_OK | W_OK)) {
        fprintf(stderr, "No accessible /dev/kvm; use the Linux x86_64 host.\n");
        return 77;
    }
    char path[4096];
    int length = snprintf(path, sizeof(path), "%s/backing-XXXXXX", argv[2]);
    REQUIRE(length > 0 && (size_t)length < sizeof(path));
    int file = mkstemp(path);
    REQUIRE(file >= 0 && unlink(path) == 0);
    struct statfs fs;
    REQUIRE(fstatfs(file, &fs) == 0 && fs.f_type != 0x01021997);
    REQUIRE(ftruncate(file, WINDOW) == 0);
    int commands[2][2], replies[2][2];
    for (int i = 0; i < 2; i++)
        REQUIRE(pipe(commands[i]) == 0 && pipe(replies[i]) == 0);
    for (int i = 0; i < 2; i++) {
        pid_t pid = fork();
        REQUIRE(pid >= 0);
        if (!pid) {
            memset(clients, 0, sizeof(clients));
            for (int j = 0; j < 2; j++) {
                close(commands[j][1]); close(replies[j][0]);
                if (i != j) { close(commands[j][0]); close(replies[j][1]); }
            }
            worker(i, commands[i][0], replies[i][1], file, native);
            _exit(1);
        }
        clients[i] = (struct client){.pid = pid, .send = commands[i][1], .receive = replies[i][0]};
    }
    for (int i = 0; i < 2; i++) { close(commands[i][0]); close(replies[i][1]); }
    for (int cycle = 0; cycle < CYCLES; cycle++) {
        restore(file, cycle, cycle == 0);
        shrink(file, PAGE + 17, cycle);
        shrink(file, 0, cycle);
    }
    restore(file, CYCLES, 0);

    /* A lost acknowledgement is not a revocation. Stop one VMM and verify
     * the file stays intact on timeout; kill AND reap it before shrinking. */
    REQUIRE(kill(clients[1].pid, SIGSTOP) == 0);
    int status;
    REQUIRE(waitpid(clients[1].pid, &status, WUNTRACED) == clients[1].pid && WIFSTOPPED(status));
    send_request(1, (struct request){.operation = OP_GUARD, .argument = PAGE + 17});
    REQUIRE(!receive_reply(1, 100));
    check_file(file, WINDOW, CYCLES, 1);
    REQUIRE(kill(clients[1].pid, SIGKILL) == 0);
    REQUIRE(waitpid(clients[1].pid, &status, 0) == clients[1].pid &&
            WIFSIGNALED(status) && WTERMSIG(status) == SIGKILL);
    clients[1].pid = 0;
    shrink(file, PAGE + 17, CYCLES);
    shrink(file, 0, CYCLES);
    rpc(0, (struct request){.operation = OP_STOP});
    REQUIRE(waitpid(clients[0].pid, &status, 0) == clients[0].pid &&
            WIFEXITED(status) && WEXITSTATUS(status) == 0);
    clients[0].pid = 0;
    REQUIRE(close(file) == 0);
    struct utsname host;
    REQUIRE(uname(&host) == 0);
    printf("{\"status\":\"passed\",\"scope\":\"%s\",\"kernel\":\"%s\","
           "\"filesystem_magic\":%ld,\"cycles\":%d,\"worker_report_cycles\":[%u,%u],"
           "\"guest_writes\":%s,\"timeout_preserved_file\":true,\"killed_and_reaped_before_shrink\":true,"
           "\"limits\":\"Synthetic mappings only; no Linux guest filesystem, metadata/locks, "
           "crash durability, coordinator recovery, arbitrary host writers, or macOS.\"}\n",
           native ? "two-native-workers-control" : "two-bare-kvm-processes", host.release,
           (long)fs.f_type, CYCLES, clients[0].cycles, clients[1].cycles, native ? "false" : "true");
    return 0;
}

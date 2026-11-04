#include <libkrun.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>

static void checked(const char *operation, int result)
{
    if (result < 0) {
        fprintf(stderr, "%s failed: %d\n", operation, result);
        exit(1);
    }
}

int main(int argc, char **argv)
{
    if (argc != 2 || strlen(argv[1]) != 49 ||
        strncmp(argv[1], "storage-contract-", 17) != 0 ||
        strspn(argv[1] + 17, "0123456789abcdef") != 32) {
        fprintf(stderr, "Expected one synthetic storage-contract run ID\n");
        return 2;
    }
    const struct rlimit no_core = {0, 0};
    if (setrlimit(RLIMIT_CORE, &no_core) != 0)
        return 1;
    checked("log level", krun_set_log_level(3));
    int ctx = krun_create_ctx();
    checked("create context", ctx);
    checked("VM configuration", krun_set_vm_config((unsigned)ctx, 1, 512));
    checked("guest root", krun_set_root((unsigned)ctx, "/guest"));
    checked("account export", krun_add_virtiofs3((unsigned)ctx, "account", "/account",
                                                64ULL << 20, false));
    checked("guest kernel", krun_set_kernel((unsigned)ctx, "/kernel/vmlinux",
                                            KRUN_KERNEL_FORMAT_ELF, NULL,
                                            "reboot=k panic=1 nomodule console=hvc0 "
                                            "rootfstype=virtiofs rw loglevel=7 no-kvmapf "
                                            "init=/init.krun"));
    /* Otherwise libkrun creates a second console reading the same stdin pipe. */
    checked("disable implicit console", krun_disable_implicit_console((unsigned)ctx));
    checked("console", krun_add_virtio_console_default((unsigned)ctx, 0, 1, 2));
    checked("working directory", krun_set_workdir((unsigned)ctx, "/"));
    const char *arguments[] = {"/probe/guest.py", argv[1], NULL};
    const char *environment[] = {"PATH=/usr/bin:/bin", "PYTHONDONTWRITEBYTECODE=1", NULL};
    checked("guest execution", krun_set_exec((unsigned)ctx, "/usr/bin/python3", arguments, environment));
    checked("start VM", krun_start_enter((unsigned)ctx));
    return 0;
}

// Native control of the actual coordinator/mapping module, without a VM.
use krun_devices::virtio::fs::linux::coordinated::Coordinator;
use std::fs::File;
use std::io::{self, BufRead, Write};
use std::os::unix::fs::OpenOptionsExt;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let parent: i32 = std::env::var("YCF_PARENT")?.parse()?;
    if unsafe { libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGKILL, 0, 0, 0) } != 0
        || unsafe { libc::getppid() } != parent
    {
        return Err("controller disappeared".into());
    }
    let root = std::env::var("YCF_ROOT")?;
    if std::fs::read_to_string(format!("{root}/../marker"))? != "ycf-native-control-v1\n" {
        return Err("not a synthetic native trial".into());
    }
    if std::env::args().nth(1).as_deref() == Some("--filesystem") {
        krun_devices::virtio::fs::linux::coordinated_native_fs::run(&root)?;
        println!("NATIVE_FS_PASS");
        io::stdout().flush()?;
        return Ok(());
    }
    let coordinator = Coordinator::connect(&root)?.ok_or("coordinator not enabled")?;
    let file = File::options()
        .read(true)
        .write(true)
        .custom_flags(libc::O_NOFOLLOW)
        .open(format!("{root}/data"))?;
    let ptr = unsafe {
        libc::mmap(
            std::ptr::null_mut(),
            16384,
            libc::PROT_NONE,
            libc::MAP_PRIVATE | libc::MAP_ANONYMOUS,
            -1,
            0,
        )
    };
    if ptr == libc::MAP_FAILED {
        return Err(io::Error::last_os_error().into());
    }
    coordinator.setup(
        file.try_clone()?,
        ptr as u64,
        16384,
        0,
        libc::PROT_READ | libc::PROT_WRITE,
    )?;
    println!("READY");
    io::stdout().flush()?;
    for line in io::stdin().lock().lines() {
        let line = line?;
        let words: Vec<_> = line.split_whitespace().collect();
        match words.as_slice() {
            ["read", offset] => {
                let offset: usize = offset.parse()?;
                if offset >= 16384 {
                    return Err("offset outside trial window".into());
                }
                println!("{}", unsafe {
                    (ptr as *const u8).add(offset).read_volatile()
                });
            }
            ["write", offset, value] => {
                let offset: usize = offset.parse()?;
                let value: u8 = value.parse()?;
                if offset >= 16384 {
                    return Err("offset outside trial window".into());
                }
                unsafe { (ptr as *mut u8).add(offset).write_volatile(value) };
                println!("OK");
            }
            ["size", size] => {
                let size: u64 = size.parse()?;
                if size > 12288 {
                    return Err("size outside native trial bounds".into());
                }
                let mut transaction = coordinator.begin()?;
                let old = file.metadata()?.len();
                if size < old {
                    transaction.resize(&file, size)?;
                }
                file.set_len(size)?;
                if size > old {
                    transaction.resize(&file, size)?;
                }
                transaction.finish()?;
                println!("OK");
            }
            ["map", start, length, offset] => {
                let start: u64 = start.parse()?;
                let length: u64 = length.parse()?;
                let offset: u64 = offset.parse()?;
                if length == 0
                    || start.checked_add(length).is_none_or(|end| end > 16384)
                    || offset.checked_add(length).is_none_or(|end| end > 16384)
                {
                    return Err("mapping outside native trial bounds".into());
                }
                coordinator.setup(
                    file.try_clone()?,
                    ptr as u64 + start,
                    length,
                    offset,
                    libc::PROT_READ | libc::PROT_WRITE,
                )?;
                println!("OK");
            }
            ["unmap", start, length] => {
                let start: u64 = start.parse()?;
                let length: u64 = length.parse()?;
                if length == 0 || start.checked_add(length).is_none_or(|end| end > 16384) {
                    return Err("unmapping outside native trial bounds".into());
                }
                coordinator.remove(ptr as u64 + start, length)?;
                println!("OK");
            }
            ["quit"] => break,
            _ => return Err("invalid native control operation".into()),
        }
        io::stdout().flush()?;
    }
    coordinator.remove(ptr as u64, 16384)?;
    if unsafe { libc::munmap(ptr, 16384) } != 0 {
        return Err(io::Error::last_os_error().into());
    }
    Ok(())
}

// Private Linux-only synthetic prototype, not a production filesystem service.
use std::fs::File;
use std::io::{self, Read, Write};
use std::os::fd::AsRawFd;
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

fn send(stream: &mut UnixStream, words: [u64; 5]) -> io::Result<()> {
    for word in words {
        stream.write_all(&word.to_le_bytes())?;
    }
    Ok(())
}

fn ack(stream: &mut UnixStream) -> io::Result<()> {
    let mut bytes = [0; 8];
    stream.read_exact(&mut bytes)?;
    let errno = u64::from_le_bytes(bytes);
    if errno == 0 {
        Ok(())
    } else {
        Err(io::Error::from_raw_os_error(libc::EIO))
    }
}

fn invalid() -> io::Error {
    io::Error::from_raw_os_error(libc::EINVAL)
}

struct Mapping {
    addr: u64,
    len: u64,
    offset: u64,
    mapped: u64,
    file: File,
    prot: i32,
    dev: u64,
    ino: u64,
}

fn anon(addr: u64, len: u64, prot: i32) -> io::Result<()> {
    if len == 0 {
        return Ok(());
    }
    // The range must already belong to this VMM's validated DAX window.
    let ptr = unsafe {
        libc::mmap(
            addr as *mut _,
            len as usize,
            prot,
            libc::MAP_PRIVATE | libc::MAP_ANONYMOUS | libc::MAP_FIXED,
            -1,
            0,
        )
    };
    if ptr == libc::MAP_FAILED {
        Err(io::Error::last_os_error())
    } else {
        Ok(())
    }
}

impl Mapping {
    fn resize(&mut self, size: u64) -> io::Result<()> {
        // This first Linux prototype deliberately requires 4 KiB host pages.
        let rounded = size.checked_add(4095).ok_or_else(invalid)? & !4095;
        let wanted = rounded.saturating_sub(self.offset).min(self.len);
        if wanted < self.mapped {
            anon(self.addr + wanted, self.mapped - wanted, self.prot)?;
        } else if wanted > self.mapped {
            let ptr = unsafe {
                libc::mmap(
                    (self.addr + self.mapped) as *mut _,
                    (wanted - self.mapped) as usize,
                    self.prot,
                    libc::MAP_SHARED | libc::MAP_FIXED,
                    self.file.as_raw_fd(),
                    (self.offset + self.mapped) as libc::off_t,
                )
            };
            if ptr == libc::MAP_FAILED {
                return Err(io::Error::last_os_error());
            }
        }
        self.mapped = wanted;
        Ok(())
    }
}

#[derive(Default)]
struct Registry {
    mappings: Vec<Mapping>,
}

impl Registry {
    fn remove(&mut self, addr: u64, len: u64) -> io::Result<()> {
        let end = addr.checked_add(len).ok_or_else(invalid)?;
        let mut next = Vec::new();
        for mapping in &self.mappings {
            let mend = mapping.addr + mapping.len;
            for (left, right) in [
                (mapping.addr, addr.min(mend)),
                (end.max(mapping.addr), mend),
            ] {
                if left >= right {
                    continue;
                }
                let skip = left - mapping.addr;
                next.push(Mapping {
                    addr: left,
                    len: right - left,
                    offset: mapping.offset + skip,
                    mapped: mapping.mapped.saturating_sub(skip).min(right - left),
                    file: mapping.file.try_clone()?,
                    prot: mapping.prot,
                    dev: mapping.dev,
                    ino: mapping.ino,
                });
            }
        }
        self.mappings = next;
        Ok(())
    }
}

pub struct Coordinator {
    socket: String,
    client: u64,
    registry: Arc<Mutex<Registry>>,
    // Every VMM retains its own open description, for its entire mapping life.
    _lease: File,
}

impl Coordinator {
    pub fn connect(root: &str) -> io::Result<Option<Self>> {
        if std::env::var("YCF_ROOT").ok().as_deref() != Some(root) {
            return Ok(None);
        }
        if unsafe { libc::sysconf(libc::_SC_PAGESIZE) } != 4096 {
            return Err(invalid());
        }
        let directory = std::env::var("YCF_CONTROL").map_err(|_| invalid())?;
        let lease = File::options()
            .read(true)
            .write(true)
            .open(Path::new(&directory).join("lifetime.lock"))?;
        if unsafe { libc::flock(lease.as_raw_fd(), libc::LOCK_SH | libc::LOCK_NB) } != 0 {
            return Err(io::Error::last_os_error());
        }
        let socket = Path::new(&directory)
            .join("control.sock")
            .to_str()
            .ok_or_else(invalid)?
            .to_owned();
        // VMMs have separate PID namespaces, so both may be PID 1.
        let client: u64 = std::env::var("YCF_CLIENT")
            .map_err(|_| invalid())?
            .parse()
            .map_err(|_| invalid())?;
        if !(1..=2).contains(&client) {
            return Err(invalid());
        }
        let registry = Arc::new(Mutex::new(Registry::default()));
        let listener_registry = registry.clone();
        let mut events = UnixStream::connect(&socket)?;
        events.set_write_timeout(Some(Duration::from_secs(10)))?;
        events.set_read_timeout(Some(Duration::from_secs(10)))?;
        send(&mut events, [1, client, 0, 0, 0])?;
        ack(&mut events)?;
        events.set_read_timeout(None)?;
        std::thread::Builder::new()
            .name("ycf-remap".into())
            .spawn(move || {
                let result = (|| -> io::Result<()> {
                    loop {
                        let mut frame = [0u8; 40];
                        events.read_exact(&mut frame)?;
                        let words: Vec<_> = frame
                            .chunks_exact(8)
                            .map(|b| u64::from_le_bytes(b.try_into().unwrap()))
                            .collect();
                        if words[0] != 3 {
                            return Err(invalid());
                        }
                        for mapping in &mut listener_registry.lock().unwrap().mappings {
                            if mapping.dev == words[2] && mapping.ino == words[3] {
                                mapping.resize(words[4])?;
                            }
                        }
                        events.write_all(&0u64.to_le_bytes())?;
                    }
                })();
                eprintln!("synthetic filesystem coordinator lost: {result:?}");
                // Do not leave a live VCPU with a detached mapping authority.
                unsafe { libc::_exit(78) }
            })?;
        Ok(Some(Self {
            socket,
            client,
            registry,
            _lease: lease,
        }))
    }

    pub fn begin(&self) -> io::Result<Transaction> {
        let mut stream = UnixStream::connect(&self.socket)?;
        stream.set_write_timeout(Some(Duration::from_secs(10)))?;
        stream.set_read_timeout(Some(Duration::from_secs(10)))?;
        send(&mut stream, [2, self.client, 0, 0, 0])?;
        ack(&mut stream)?;
        Ok(Transaction {
            stream,
            client: self.client,
            complete: false,
        })
    }

    pub fn setup(&self, file: File, addr: u64, len: u64, offset: u64, prot: i32) -> io::Result<()> {
        if (addr | len | offset) & 4095 != 0 || len == 0 {
            return Err(invalid());
        }
        addr.checked_add(len).ok_or_else(invalid)?;
        offset
            .checked_add(len)
            .filter(|end| *end <= i64::MAX as u64)
            .ok_or_else(invalid)?;
        let transaction = self.begin()?;
        let metadata = file.metadata()?;
        let mut registry = self.registry.lock().unwrap();
        registry.remove(addr, len)?;
        anon(addr, len, prot)?;
        let mut mapping = Mapping {
            addr,
            len,
            offset,
            mapped: 0,
            file,
            prot,
            dev: metadata.dev(),
            ino: metadata.ino(),
        };
        mapping.resize(metadata.len())?;
        registry.mappings.push(mapping);
        drop(registry);
        eprintln!("YCF_SETUPMAPPING addr={addr:x} len={len}");
        transaction.finish()
    }

    pub fn remove(&self, addr: u64, len: u64) -> io::Result<()> {
        if (addr | len) & 4095 != 0 {
            return Err(invalid());
        }
        addr.checked_add(len).ok_or_else(invalid)?;
        let transaction = self.begin()?;
        self.registry.lock().unwrap().remove(addr, len)?;
        anon(addr, len, libc::PROT_NONE)?;
        transaction.finish()
    }
}

pub struct Transaction {
    stream: UnixStream,
    client: u64,
    complete: bool,
}

impl Transaction {
    pub fn resize(&mut self, file: &File, size: u64) -> io::Result<()> {
        let metadata = file.metadata()?;
        send(
            &mut self.stream,
            [3, self.client, metadata.dev(), metadata.ino(), size],
        )?;
        ack(&mut self.stream)
    }

    pub fn finish(mut self) -> io::Result<()> {
        send(&mut self.stream, [4, self.client, 0, 0, 0])?;
        ack(&mut self.stream)?;
        self.complete = true;
        Ok(())
    }
}

impl Drop for Transaction {
    fn drop(&mut self) {
        if !self.complete {
            // A partially applied mapping transition is not recoverable by
            // returning one failed syscall. The trial supervisor reaps all VMMs.
            unsafe { libc::_exit(78) }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::FileExt;

    #[test]
    fn guards_discard_writes_after_shrink_and_preserve_valid_pages() {
        let unique = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path = std::env::temp_dir().join(format!("ycf-test-{}-{unique}", std::process::id()));
        let file = File::options()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)
            .unwrap();
        std::fs::remove_file(path).unwrap();
        file.set_len(12288).unwrap();
        let ptr = unsafe {
            libc::mmap(
                std::ptr::null_mut(),
                16384,
                libc::PROT_READ | libc::PROT_WRITE,
                libc::MAP_ANONYMOUS | libc::MAP_PRIVATE,
                -1,
                0,
            )
        };
        assert_ne!(ptr, libc::MAP_FAILED);
        let mut mapping = Mapping {
            addr: ptr as u64,
            len: 16384,
            offset: 0,
            mapped: 0,
            file,
            prot: libc::PROT_READ | libc::PROT_WRITE,
            dev: 0,
            ino: 0,
        };
        mapping.resize(12288).unwrap();
        unsafe { (ptr as *mut u8).write_volatile(71) };
        mapping.resize(17).unwrap();
        mapping.file.set_len(17).unwrap();
        unsafe { (ptr as *mut u8).add(8192).write_volatile(99) };
        mapping.file.set_len(12288).unwrap();
        mapping.resize(12288).unwrap();
        assert_eq!(unsafe { (ptr as *const u8).read_volatile() }, 71);
        assert_eq!(unsafe { (ptr as *const u8).add(8192).read_volatile() }, 0);
        let mut byte = [0];
        mapping.file.read_exact_at(&mut byte, 8192).unwrap();
        assert_eq!(byte, [0]);
        unsafe {
            libc::munmap(ptr, 16384);
        }
    }
}

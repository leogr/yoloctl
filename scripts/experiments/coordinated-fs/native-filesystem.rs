// Native semantic control through the same AugmentFs/PassthroughFs stack as
// the virtio device. Compiled only under libkrun's existing test_utils feature.
use super::super::augment_fs::AugmentFs;
use super::super::filesystem::{Context, Extensions, FileSystem, FsOptions, ZeroCopyReader};
use super::super::fuse::{FileLock, LkIn, LK_FLOCK, ROOT_ID};
use super::super::inode_alloc::InodeAllocator;
use super::super::{fuse, server::Server};
use super::passthrough::{Config, PassthroughFs};
use crate::virtio::descriptor_utils::{create_descriptor_chain, DescriptorType, Reader, Writer};
use std::fs::File;
use std::io::{self, Cursor, Read};
use std::os::unix::fs::FileExt;
use std::sync::atomic::AtomicI32;
use std::sync::Arc;
use vm_memory::{ByteValued, Bytes, GuestAddress, GuestMemoryMmap};

type Fs = AugmentFs<PassthroughFs>;

fn ctx() -> Context {
    Context {
        uid: 0,
        gid: 0,
        pid: 123,
    }
}

struct Input(Cursor<Vec<u8>>);
impl Read for Input {
    fn read(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
        self.0.read(bytes)
    }
}
impl ZeroCopyReader for Input {
    fn read_to(&mut self, file: &File, count: usize, offset: u64) -> io::Result<usize> {
        let position = self.0.position() as usize;
        let bytes = self.0.get_ref();
        let count = count.min(bytes.len() - position);
        let written = file.write_at(&bytes[position..position + count], offset)?;
        self.0.set_position((position + written) as u64);
        Ok(written)
    }
}

fn peer(root: &str, identity: u64) -> io::Result<Fs> {
    // This dedicated helper has no other thread that reads these variables.
    std::env::set_var("YCF_CLIENT", identity.to_string());
    let allocator = Arc::new(InodeAllocator::new());
    let filesystem = PassthroughFs::new(
        Config {
            root_dir: root.to_owned(),
            ..Config::default()
        },
        allocator.clone(),
    )?;
    let filesystem = AugmentFs::new(filesystem, &allocator, Vec::new());
    let wanted = filesystem
        .init(FsOptions::COORDINATED_IO | FsOptions::POSIX_LOCKS | FsOptions::FLOCK_LOCKS)?;
    assert!(wanted
        .contains(FsOptions::COORDINATED_IO | FsOptions::POSIX_LOCKS | FsOptions::FLOCK_LOCKS));
    Ok(filesystem)
}

fn lock(handle: u64, owner: u64, kind: i32, flock: bool) -> LkIn {
    LkIn {
        fh: handle,
        owner,
        lk: FileLock {
            start: 7,
            end: 7,
            type_: kind as u32,
            pid: 123,
        },
        lk_flags: if flock { LK_FLOCK } else { 0 },
        padding: 0,
    }
}

fn conflict(result: io::Result<()>) {
    let error = result.expect_err("peer lock must conflict");
    assert!([Some(libc::EAGAIN), Some(libc::EACCES)].contains(&error.raw_os_error()));
}

pub fn run(root: &str) -> io::Result<()> {
    let a = peer(root, 1)?;
    let b = peer(root, 2)?;
    let (entry, handle, _) = a.create(
        ctx(),
        ROOT_ID,
        c"semantic-data",
        0o600,
        false,
        (libc::O_RDWR | libc::O_CREAT | libc::O_EXCL | libc::O_APPEND) as u32,
        0,
        Extensions::default(),
    )?;
    let ai = entry.inode;
    let ah = handle.unwrap();
    let bi = b.lookup(ctx(), ROOT_ID, c"semantic-data")?.inode;
    let bh = b
        .open(ctx(), bi, false, (libc::O_RDWR | libc::O_APPEND) as u32)?
        .0
        .unwrap();
    let mut expected = Vec::new();
    for step in 0..64 {
        let (fs, inode, handle) = if step % 2 == 0 {
            (&a, ai, ah)
        } else {
            (&b, bi, bh)
        };
        let bytes = format!("{step}: native append\n").into_bytes();
        expected.extend(&bytes);
        let result = fs.coordinated_write(
            ctx(),
            inode,
            handle,
            Input(Cursor::new(bytes.clone())),
            bytes.len() as u32,
            0,
            false,
            (libc::O_RDWR | libc::O_APPEND) as u32,
        )?;
        assert_eq!(result, (bytes.len(), expected.len() as u64));
        assert_eq!(std::fs::read(format!("{root}/semantic-data"))?, expected);
    }
    println!("NATIVE_FS append placement and completed offsets pass");

    a.setlk(ai, lock(ah, 11, libc::F_WRLCK, false), false)?;
    conflict(b.setlk(bi, lock(bh, 22, libc::F_WRLCK, false), false));
    assert_eq!(
        b.getlk(bi, lock(bh, 22, libc::F_WRLCK, false))?.type_,
        libc::F_WRLCK as u32
    );
    let other = a.open(ctx(), ai, false, libc::O_RDWR as u32)?.0.unwrap();
    a.flush(ctx(), ai, other, 33)?; // A different virtual process cannot release owner 11.
    conflict(b.setlk(bi, lock(bh, 22, libc::F_WRLCK, false), false));
    a.flush(ctx(), ai, other, 11)?; // Any independent close by owner 11 must release its POSIX locks.
    b.setlk(bi, lock(bh, 22, libc::F_WRLCK, false), false)?;
    b.setlk(bi, lock(bh, 22, libc::F_UNLCK, false), false)?;
    println!("NATIVE_FS POSIX conflicts, GETLK and close ownership pass");

    a.setlk(ai, lock(ah, 11, libc::F_WRLCK, true), false)?;
    conflict(b.setlk(bi, lock(bh, 22, libc::F_WRLCK, true), false));
    a.flush(ctx(), ai, ah, 11)?; // A close with another dup still alive only sends FLUSH.
    conflict(b.setlk(bi, lock(bh, 22, libc::F_WRLCK, true), false));
    a.release(ctx(), ai, 0, other, false, false, None)?;
    conflict(b.setlk(bi, lock(bh, 22, libc::F_WRLCK, true), false));
    a.release(ctx(), ai, 0, ah, false, true, Some(11))?;
    b.setlk(bi, lock(bh, 22, libc::F_WRLCK, true), false)?;
    b.setlk(bi, lock(bh, 22, libc::F_UNLCK, true), false)?;
    println!("NATIVE_FS flock independent/duplicate/final-close lifetimes pass");

    assert_eq!(
        b.setlk(bi, lock(bh, 22, libc::F_WRLCK, false), true)
            .unwrap_err()
            .raw_os_error(),
        Some(libc::EOPNOTSUPP)
    );
    assert_eq!(
        b.write(
            ctx(),
            bi,
            bh,
            Input(Cursor::new(vec![99])),
            1,
            0,
            None,
            false,
            false,
            0
        )
        .unwrap_err()
        .raw_os_error(),
        Some(libc::EOPNOTSUPP)
    );
    println!("NATIVE_FS unsupported blocking locks and uncoordinated writes are rejected");
    b.release(ctx(), bi, 0, bh, false, false, None)?;
    wire(a, b, root)?;
    Ok(())
}

// Use the actual virtio descriptor decoder and FUSE dispatcher. This is a
// protocol component control: it does not emulate a Linux guest's VFS or DAX.
fn request(server: &Server<Fs>, opcode: fuse::Opcode, inode: u64, body: &[u8]) -> (i32, Vec<u8>) {
    let header = fuse::InHeader {
        len: (std::mem::size_of::<fuse::InHeader>() + body.len()) as u32,
        opcode: opcode as u32,
        unique: 91,
        nodeid: inode,
        pid: 123,
        ..Default::default()
    };
    let mut input = header.as_slice().to_vec();
    input.extend_from_slice(body);
    let memory_size = (input.len() + 0x3000 + 4095) & !4095;
    let memory = GuestMemoryMmap::from_ranges(&[(GuestAddress(0), memory_size)]).unwrap();
    let address = 0x1000;
    memory.write_slice(&input, GuestAddress(address)).unwrap();
    let chain = create_descriptor_chain(
        &memory,
        GuestAddress(0),
        GuestAddress(address),
        vec![
            (DescriptorType::Readable, input.len() as u32),
            (DescriptorType::Writable, 4096),
        ],
        0,
    )
    .unwrap();
    let count = server
        .handle_message(
            Reader::new(&memory, chain.clone()).unwrap(),
            Writer::new(&memory, chain).unwrap(),
            false,
            &None,
            &Arc::new(AtomicI32::new(0)),
        )
        .expect("FUSE request decoding failed");
    let output_address = GuestAddress(address + input.len() as u64);
    let output: fuse::OutHeader = memory.read_obj(output_address).unwrap();
    assert_eq!(output.unique, 91);
    assert_eq!(output.len as usize, count);
    let mut output_body = vec![0; count - std::mem::size_of::<fuse::OutHeader>()];
    memory
        .read_slice(&mut output_body, GuestAddress(output_address.0 + 16))
        .unwrap();
    (output.error, output_body)
}

fn initialize(server: &Server<Fs>, coordinated: bool) -> (i32, Vec<u8>) {
    let mut flags = FsOptions::INIT_EXT
        | FsOptions::POSIX_LOCKS
        | FsOptions::FLOCK_LOCKS
        | FsOptions::ATOMIC_O_TRUNC
        | FsOptions::ASYNC_DIO;
    if coordinated {
        flags |= FsOptions::COORDINATED_IO;
    }
    let input = fuse::InitInCompat {
        major: 7,
        minor: 42,
        flags: flags.bits() as u32,
        ..Default::default()
    };
    let extra = fuse::InitInExt {
        flags2: (flags.bits() >> 32) as u32,
        ..Default::default()
    };
    let mut body = input.as_slice().to_vec();
    body.extend_from_slice(extra.as_slice());
    request(server, fuse::Opcode::Init, 0, &body)
}

fn create_wire(server: &Server<Fs>, name: &[u8]) -> (u64, u64) {
    let input = fuse::CreateIn {
        flags: (libc::O_RDWR | libc::O_CREAT | libc::O_EXCL | libc::O_APPEND) as u32,
        mode: 0o600,
        ..Default::default()
    };
    let mut body = input.as_slice().to_vec();
    body.extend_from_slice(name);
    let (error, output) = request(server, fuse::Opcode::Create, ROOT_ID, &body);
    assert_eq!(error, 0);
    let entry_size = std::mem::size_of::<fuse::EntryOut>();
    assert_eq!(
        output.len(),
        entry_size + std::mem::size_of::<fuse::OpenOut>()
    );
    (
        u64::from_le_bytes(output[0..8].try_into().unwrap()),
        u64::from_le_bytes(output[entry_size..entry_size + 8].try_into().unwrap()),
    )
}

fn write_wire(
    server: &Server<Fs>,
    inode: u64,
    handle: u64,
    data: &[u8],
    coordinated: bool,
) -> (i32, Vec<u8>) {
    let input = fuse::WriteIn {
        fh: handle,
        size: data.len() as u32,
        write_flags: if coordinated { 1 << 31 } else { 0 },
        flags: (libc::O_RDWR | libc::O_APPEND) as u32,
        ..Default::default()
    };
    let mut body = input.as_slice().to_vec();
    body.extend_from_slice(data);
    request(server, fuse::Opcode::Write, inode, &body)
}

fn wire(a: Fs, b: Fs, root: &str) -> io::Result<()> {
    let a = Server::new(a);
    let b = Server::new(b);
    let mut max_write = 0;
    assert_eq!(initialize(&a, false).0, -libc::EPROTO);
    for server in [&a, &b] {
        let (error, bytes) = initialize(server, true);
        assert_eq!(error, 0);
        assert_eq!(bytes.len(), std::mem::size_of::<fuse::InitOut>());
        let low = u32::from_le_bytes(bytes[12..16].try_into().unwrap()) as u64;
        let high = u32::from_le_bytes(bytes[32..36].try_into().unwrap()) as u64;
        let flags = FsOptions::from_bits_truncate(low | (high << 32));
        let advertised_write = u32::from_le_bytes(bytes[20..24].try_into().unwrap()) as usize;
        assert_eq!(advertised_write, 1 << 20);
        max_write = advertised_write;
        assert!(flags
            .contains(FsOptions::COORDINATED_IO | FsOptions::POSIX_LOCKS | FsOptions::FLOCK_LOCKS));
        assert!(!flags.intersects(FsOptions::ATOMIC_O_TRUNC | FsOptions::ASYNC_DIO));
    }
    let (ai, ah) = create_wire(&a, b"wire-data\0");
    let (error, lookup) = request(&b, fuse::Opcode::Lookup, ROOT_ID, b"wire-data\0");
    assert_eq!(error, 0);
    let bi = u64::from_le_bytes(lookup[0..8].try_into().unwrap());
    let open = fuse::OpenIn {
        flags: (libc::O_RDWR | libc::O_APPEND) as u32,
        ..Default::default()
    };
    let (error, opened) = request(&b, fuse::Opcode::Open, bi, open.as_slice());
    assert_eq!(error, 0);
    let bh = u64::from_le_bytes(opened[0..8].try_into().unwrap());
    let mut expected = Vec::new();
    for step in 0..32 {
        let (server, inode, handle) = if step % 2 == 0 {
            (&a, ai, ah)
        } else {
            (&b, bi, bh)
        };
        let bytes = format!("{step}: wire append\n").into_bytes();
        expected.extend_from_slice(&bytes);
        let (error, written) = write_wire(server, inode, handle, &bytes, true);
        assert_eq!(error, 0);
        assert_eq!(written.len(), 16); // 8-byte WriteOut plus actual end offset.
        assert_eq!(
            u32::from_le_bytes(written[0..4].try_into().unwrap()) as usize,
            bytes.len()
        );
        assert_eq!(
            u64::from_le_bytes(written[8..16].try_into().unwrap()) as usize,
            expected.len()
        );
        assert_eq!(std::fs::read(format!("{root}/wire-data"))?, expected);
    }
    assert_eq!(
        write_wire(&a, ai, ah, b"rejected", false).0,
        -libc::EOPNOTSUPP
    );
    let alock = lock(ah, 111, libc::F_WRLCK, false);
    let block = lock(bh, 222, libc::F_WRLCK, false);
    assert_eq!(request(&a, fuse::Opcode::Setlk, ai, alock.as_slice()).0, 0);
    assert!([-libc::EAGAIN, -libc::EACCES]
        .contains(&request(&b, fuse::Opcode::Setlk, bi, block.as_slice()).0));
    let (error, conflict) = request(&b, fuse::Opcode::Getlk, bi, block.as_slice());
    assert_eq!(error, 0);
    assert_eq!(
        u32::from_le_bytes(conflict[16..20].try_into().unwrap()),
        libc::F_WRLCK as u32
    );
    assert_eq!(
        request(&b, fuse::Opcode::Setlkw, bi, block.as_slice()).0,
        -libc::EOPNOTSUPP
    );
    println!("NATIVE_FS wire negotiation, extended append replies and remote lock dispatch pass");

    // Deliberately schedule the requests from two hypothetical large syscalls
    // A0,B0,A1,B1,A2,B2. Linux's direct-I/O loop can emit multiple requests for
    // one syscall; the current protocol has no field tying those requests
    // together. This is a deterministic backend counterexample, not a guest
    // syscall test or a passing whole-write atomicity result.
    let (large_ai, large_ah) = create_wire(&a, b"fragmented-appends\0");
    let (error, lookup) = request(&b, fuse::Opcode::Lookup, ROOT_ID, b"fragmented-appends\0");
    assert_eq!(error, 0);
    let large_bi = u64::from_le_bytes(lookup[0..8].try_into().unwrap());
    let (error, opened) = request(&b, fuse::Opcode::Open, large_bi, open.as_slice());
    assert_eq!(error, 0);
    let large_bh = u64::from_le_bytes(opened[0..8].try_into().unwrap());
    let mut fragmented = Vec::new();
    for size in [max_write, max_write, 17] {
        for (server, inode, handle, byte) in [
            (&a, large_ai, large_ah, b'A'),
            (&b, large_bi, large_bh, b'B'),
        ] {
            let data = vec![byte; size];
            fragmented.extend_from_slice(&data);
            let (error, output) = write_wire(server, inode, handle, &data, true);
            assert_eq!(error, 0);
            assert_eq!(
                u32::from_le_bytes(output[0..4].try_into().unwrap()) as usize,
                size
            );
            assert_eq!(
                u64::from_le_bytes(output[8..16].try_into().unwrap()) as usize,
                fragmented.len()
            );
        }
    }
    let contents = std::fs::read(format!("{root}/fragmented-appends"))?;
    assert_eq!(contents, fragmented);
    let record_size = max_write * 2 + 17;
    let mut contiguous = vec![b'A'; record_size];
    contiguous.extend(vec![b'B'; record_size]);
    assert_ne!(contents, contiguous);
    contiguous.reverse();
    assert_ne!(contents, contiguous);
    println!("NATIVE_LIMIT append serialization covers one FUSE request, not a fragmented guest syscall: two 2097169-byte records interleave at the 1048576-byte request boundary");

    // An ordinary, uncoordinated export must retain the original 8-byte reply.
    let legacy_root = format!("{root}/legacy");
    std::fs::create_dir(&legacy_root)?;
    let allocator = Arc::new(InodeAllocator::new());
    let fs = PassthroughFs::new(
        Config {
            root_dir: legacy_root.clone(),
            ..Default::default()
        },
        allocator.clone(),
    )?;
    let legacy = Server::new(AugmentFs::new(fs, &allocator, Vec::new()));
    assert_eq!(initialize(&legacy, false).0, 0);
    let (inode, handle) = create_wire(&legacy, b"ordinary\0");
    let (error, result) = write_wire(&legacy, inode, handle, b"legacy", false);
    assert_eq!(error, 0);
    assert_eq!(result.len(), 8);
    assert_eq!(
        write_wire(&legacy, inode, handle, b"invalid", true).0,
        -libc::EINVAL
    );
    assert_eq!(std::fs::read(format!("{legacy_root}/ordinary"))?, b"legacy");
    println!(
        "NATIVE_FS ordinary-export reply compatibility and unnegotiated-extension rejection pass"
    );
    Ok(())
}

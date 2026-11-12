//! Fixed WASI command: UTF-8 source on stdin (read to its end), width in
//! argv[1]. The Go owner supplies deadlines, guest-memory and output bounds;
//! the guest memory limit also bounds the source this command can hold.
#![forbid(unsafe_code)]

use std::io::{Read, Write};

fn main() {
    let mut args = std::env::args().skip(1);
    let width = match args.next().and_then(|s| s.parse::<usize>().ok()) {
        Some(n @ 1..=4096) if args.next().is_none() => n,
        _ => std::process::exit(2),
    };
    let mut source = String::new();
    if std::io::stdin().read_to_string(&mut source).is_err() || source.is_empty() {
        std::process::exit(2);
    }
    let options = mermaid_text::RenderOptions {
        max_width: Some(width),
        max_width_strict: true,
        ..Default::default()
    };
    match mermaid_text::render_with_options(&source, &options) {
        Ok(drawing) => {
            if std::io::stdout().write_all(drawing.as_bytes()).is_err() {
                std::process::exit(5);
            }
        }
        Err(mermaid_text::Error::TooWide { actual, .. }) => {
            eprintln!("{actual}");
            std::process::exit(4);
        }
        Err(_) => std::process::exit(3),
    }
}

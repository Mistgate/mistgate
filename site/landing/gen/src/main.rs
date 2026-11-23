use mistgate_site_gen::{run, Options};
use std::path::PathBuf;
use std::process::ExitCode;

const USAGE: &str = "usage: mistgate-site-gen --build <dir> --out <dir> [--docs-dist <dir>]

  --build       the board pipeline's output (manifest.json, pages/, assets/)
  --out         where to write the site; emptied first
  --docs-dist   the dist/ of the docs generator (node site/build.mjs), to check the links into the docs";

fn main() -> ExitCode {
    let (mut build, mut out, mut docs_dist) = (None, None, None);
    let mut args = std::env::args().skip(1);
    while let Some(a) = args.next() {
        let slot = match a.as_str() {
            "--build" => &mut build,
            "--out" => &mut out,
            "--docs-dist" => &mut docs_dist,
            "-h" | "--help" => {
                println!("{USAGE}");
                return ExitCode::SUCCESS;
            }
            _ => {
                eprintln!("unknown argument {a:?}\n{USAGE}");
                return ExitCode::from(2);
            }
        };
        let Some(v) = args.next() else {
            eprintln!("{a} needs a value\n{USAGE}");
            return ExitCode::from(2);
        };
        *slot = Some(PathBuf::from(v));
    }
    let (Some(build), Some(out)) = (build, out) else {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    };
    match run(&Options { build, out: out.clone(), docs_dist }) {
        Ok(r) => {
            println!("wrote {} pages, {} files -> {}", r.pages, r.files, out.display());
            if !r.todo.is_empty() {
                println!("warning: {} page(s) marked todo (built with what they have): {}", r.todo.len(), r.todo.join(", "));
            }
            if r.unchecked_docs_links > 0 {
                println!("warning: {} link(s) into the docs were not checked (pass --docs-dist <dist of node site/build.mjs>)", r.unchecked_docs_links);
            }
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("error: {e}");
            ExitCode::FAILURE
        }
    }
}

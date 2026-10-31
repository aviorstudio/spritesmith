mod client;
mod token;
type Result<T> = std::result::Result<T, Box<dyn std::error::Error + Send + Sync>>;

const FLAGS: &[&str] = &["model", "operation-id", "api", "out", "kind", "format"];
fn help() -> String {
    format!(
        "usage: spritesmith COMMAND [flags]\n\nCommands:\n  prompt PROMPT               make a sprite from a text prompt through the API\n  version                     print version and source revision\n\nFlags (before PROMPT):\n  --kind vector               SVG animation creation (default)\n  --format png|svg            download format, default png\n  --model haiku|opus          reasoning model, default haiku\n  --operation-id ID           retain this ID for safe retries\n  --api URL                   API base, default http://127.0.0.1:8080\n  --out FILE                  output path, default spritesmith.png (or spritesmith.svg)\n\nAuthentication:\n  Create a Clerk API key for your spritesmith account. An explicitly configured local development API needs none.\n  export SPRITESMITH_API_TOKEN=<token>\n  # or: install -D -m 600 /dev/null {0} && $EDITOR {0}\n  SPRITESMITH_API_TOKEN wins over the file. There is no --token flag because arguments are visible in process lists and shell history.\n  Tokens are sent only over https or to a loopback address.\n\nOutput:\n  Each run downloads one PNG or SVG. An existing file is never overwritten.\n  Use the web app to attach visual references to prompts.\n",
        token::hint()
    )
}

fn parse(args: &[String]) -> Result<client::Request> {
    let mut request = client::Request {
        model: "haiku".into(),
        operation: String::new(),
        api: std::env::var("SPRITESMITH_API_BASE")
            .ok()
            .filter(|s| !s.is_empty())
            .unwrap_or_else(|| "http://127.0.0.1:8080".into()),
        out: String::new(),
        kind: "vector".into(),
        format: "png".into(),
        prompt: String::new(),
    };
    let mut index = 0;
    let mut terminated = false;
    while index < args.len() {
        let arg = &args[index];
        if arg == "--" {
            terminated = true;
            index += 1;
            break;
        }
        if !arg.starts_with('-') || arg == "-" {
            break;
        }
        let (name, value) = arg
            .trim_start_matches('-')
            .split_once('=')
            .map_or((arg.trim_start_matches('-'), None), |(k, v)| (k, Some(v)));
        if !FLAGS.contains(&name) {
            return Err(format!("unknown flag {arg:?}").into());
        }
        let value = if let Some(value) = value {
            value.to_owned()
        } else {
            index += 1;
            args.get(index)
                .ok_or_else(|| format!("{arg} needs a value"))?
                .clone()
        };
        match name {
            "model" => request.model = value,
            "operation-id" => request.operation = value,
            "api" => request.api = value,
            "out" => request.out = value,
            "kind" => request.kind = value,
            "format" => request.format = value,
            _ => unreachable!(),
        };
        index += 1;
    }
    if !terminated {
        for arg in args[index..].iter().skip(1) {
            if arg.starts_with('-')
                && FLAGS.contains(&arg.trim_start_matches('-').split('=').next().unwrap_or(""))
            {
                return Err(
                    format!("misplaced flag {arg:?}: flags must come before PROMPT").into(),
                );
            }
        }
    }
    request.prompt = args[index..].join(" ").trim().into();
    if request.prompt.is_empty() {
        return Err("usage: spritesmith prompt [flags] PROMPT".into());
    }
    if request.out.trim().is_empty() {
        request.out = if request.format == "svg" {
            "spritesmith.svg"
        } else {
            "spritesmith.png"
        }
        .into();
    }
    if request.kind.is_empty() {
        request.kind = "vector".into();
    }
    if request.format.is_empty() {
        request.format = "png".into();
    }
    Ok(request)
}

fn main() {
    let args: Vec<_> = std::env::args().skip(1).collect();
    match args.first().map(String::as_str) {
        Some("--version" | "version") => println!(
            "spritesmith {} ({})",
            env!("SPRITESMITH_VERSION"),
            env!("SPRITESMITH_REVISION")
        ),
        Some("help" | "--help" | "-h") => print!("{}", help()),
        Some("prompt") => {
            let request = match parse(&args[1..]) {
                Ok(value) => value,
                Err(error) => {
                    eprintln!("{error}");
                    std::process::exit(2);
                }
            };
            let result = (|| {
                let credential = token::load()?;
                if !credential.warning.is_empty() {
                    eprintln!("warning: {}", credential.warning);
                }
                client::process(&request, &credential)
            })();
            match result {
                Ok(()) => println!("{}", request.out),
                Err(error) => {
                    eprintln!("{error}");
                    std::process::exit(1);
                }
            }
        }
        _ => {
            eprint!("{}", help());
            std::process::exit(2);
        }
    }
}

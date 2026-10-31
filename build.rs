use std::{env, process::Command};
fn main() {
    for name in ["VERSION", "COMMIT"] {
        println!("cargo:rerun-if-env-changed={name}");
    }
    let revision = env::var("COMMIT")
        .ok()
        .or_else(|| {
            let result = Command::new("git")
                .args(["rev-parse", "HEAD"])
                .output()
                .ok()?;
            result
                .status
                .success()
                .then(|| String::from_utf8_lossy(&result.stdout).trim().to_owned())
        })
        .unwrap_or_else(|| "unknown".into());
    for (key, value) in [
        (
            "SPRITESMITH_VERSION",
            env::var("VERSION").unwrap_or_else(|_| "dev".into()),
        ),
        ("SPRITESMITH_REVISION", revision),
    ] {
        assert!(!value.contains(['\r', '\n']));
        println!("cargo:rustc-env={key}={value}");
    }
    println!("cargo:rerun-if-changed=.git");
}

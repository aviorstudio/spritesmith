use crate::Result;
use std::{
    fs,
    io::Read,
    path::{Path, PathBuf},
};

pub const ENV: &str = "SPRITESMITH_API_TOKEN";
#[derive(Default)]
pub struct Credential {
    pub value: String,
    pub source: String,
    pub warning: String,
}

pub fn path() -> Option<PathBuf> {
    let env = |name| {
        std::env::var_os(name)
            .filter(|v| !v.is_empty())
            .map(PathBuf::from)
    };
    let root = if cfg!(target_os = "windows") {
        env("APPDATA")
    } else if cfg!(target_os = "macos") {
        env("HOME").map(|p| p.join("Library/Application Support"))
    } else {
        env("XDG_CONFIG_HOME").or_else(|| env("HOME").map(|p| p.join(".config")))
    };
    root.map(|p| p.join("spritesmith/token"))
}

pub fn hint() -> String {
    path()
        .unwrap_or_else(|| PathBuf::from("<config dir>/spritesmith/token"))
        .display()
        .to_string()
}

pub fn validate(value: &str, source: &str) -> Result<()> {
    if value.len() > 256 {
        return Err(format!(
            "the spritesmith API token in {source} is {} bytes; that is not a token",
            value.len()
        )
        .into());
    }
    if value.chars().any(|c| c <= ' ' || c == '\u{7f}') {
        return Err(format!("the spritesmith API token in {source} contains whitespace or control characters; it should be a single line").into());
    }
    Ok(())
}

fn permission_warning(path: &Path) -> String {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Ok(info) = fs::metadata(path)
            && info.permissions().mode() & 0o077 != 0
        {
            return format!(
                "{} is readable by other users on this machine; run: chmod 600 {}",
                path.display(),
                path.display()
            );
        }
    }
    #[cfg(not(unix))]
    let _ = path;
    String::new()
}

pub fn load_from(value: &str, path: Option<&Path>) -> Result<Credential> {
    let value = value.trim();
    if !value.is_empty() {
        validate(value, &format!("${ENV}"))?;
        return Ok(Credential {
            value: value.into(),
            source: format!("${ENV}"),
            warning: path
                .filter(|p| p.exists())
                .map(|p| format!("{} is being ignored because {ENV} is set", p.display()))
                .unwrap_or_default(),
        });
    }
    let Some(path) = path else {
        return Ok(Credential::default());
    };
    let file = match fs::File::open(path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            return Ok(Credential::default());
        }
        Err(error) => {
            return Err(format!(
                "read the spritesmith API token from {}: {error}",
                path.display()
            )
            .into());
        }
    };
    let mut data = String::new();
    file.take(4097)
        .read_to_string(&mut data)
        .map_err(|_| format!("could not read a valid token from {}", path.display()))?;
    if data.len() > 4096 {
        return Err(format!("token file is oversized: {}", path.display()).into());
    }
    let value = data.trim();
    if value.is_empty() {
        return Ok(Credential::default());
    }
    validate(value, &path.display().to_string())?;
    Ok(Credential {
        value: value.into(),
        source: path.display().to_string(),
        warning: permission_warning(path),
    })
}

pub fn load() -> Result<Credential> {
    load_from(&std::env::var(ENV).unwrap_or_default(), path().as_deref())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn validation_never_echoes_secret_material() {
        for value in [
            "secret token".into(),
            "secret\nsecond".into(),
            "secret\tline".into(),
            "x".repeat(257),
        ] {
            let error = validate(&value, "fixture").unwrap_err().to_string();
            assert!(!error.contains(&value));
            assert!(error.contains("fixture"));
        }
        assert!(validate(&"x".repeat(256), "fixture").is_ok());
        assert_eq!(
            load_from("  fixture-token \n", None).unwrap().value,
            "fixture-token"
        );
        assert!(load_from("", None).unwrap().value.is_empty());
    }
}

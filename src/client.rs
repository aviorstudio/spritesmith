use crate::{Result, token::Credential};
use reqwest::{
    Method, StatusCode, Url,
    blocking::{Client, Response},
    header::{ACCEPT, CONTENT_TYPE, LOCATION},
};
use serde_json::Value;
use std::{
    fs::{self, OpenOptions},
    io::{self, Read, Write},
    net::IpAddr,
    path::Path,
    time::{Duration, Instant},
};

pub struct Request {
    pub model: String,
    pub operation: String,
    pub api: String,
    pub out: String,
    pub kind: String,
    pub format: String,
    pub prompt: String,
}

pub fn check_transport(url: &Url) -> Result<()> {
    let host = url.host_str().unwrap_or("").trim_matches(['[', ']']);
    if url.scheme() == "https"
        || (url.scheme() == "http"
            && (host.eq_ignore_ascii_case("localhost")
                || host.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())))
    {
        return Ok(());
    }
    Err("refusing to send the spritesmith API token over plaintext to a remote host; configure an https:// API or unset SPRITESMITH_API_TOKEN".into())
}

pub fn check_output(path: &Path) -> Result<()> {
    if path
        .as_os_str()
        .to_string_lossy()
        .ends_with(['/', std::path::MAIN_SEPARATOR])
    {
        return Err(format!(
            "--out {} names a directory; give a file path instead",
            path.display()
        )
        .into());
    }
    match fs::symlink_metadata(path) {
        Ok(info) if info.is_dir() => Err(format!(
            "{} is a directory; give a file path instead",
            path.display()
        )
        .into()),
        Ok(_) => Err(format!(
            "{} already exists, and spritesmith will not overwrite it",
            path.display()
        )
        .into()),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error.into()),
    }
}

pub fn write_output(path: &Path, body: &mut impl Read) -> Result<()> {
    if let Some(parent) = path.parent().filter(|p| !p.as_os_str().is_empty()) {
        fs::create_dir_all(parent)?;
    }
    let mut output = OpenOptions::new().write(true).create_new(true).open(path)?;
    let result = io::copy(body, &mut output)
        .and_then(|_| output.flush())
        .and_then(|_| output.sync_all());
    drop(output);
    if let Err(error) = result {
        if let Err(remove) = fs::remove_file(path) {
            return Err(
                format!("{error} (and incomplete output could not be removed: {remove})").into(),
            );
        }
        return Err(error.into());
    }
    Ok(())
}

struct Session<'a> {
    client: &'a Client,
    credential: &'a Credential,
    deadline: Instant,
}

impl Session<'_> {
    fn send(
        &self,
        method: Method,
        url: &str,
        body: &[u8],
        content_type: &str,
        operation: &str,
    ) -> Result<Response> {
        let Self {
            client,
            credential,
            deadline,
        } = self;
        let mut url = Url::parse(url)?;
        let origin = url.host_str().unwrap_or("").to_owned();
        let mut method = method;
        let mut body = body;
        for hop in 0..=10 {
            if !credential.value.is_empty() {
                check_transport(&url)?;
                if url.host_str() != Some(origin.as_str()) {
                    return Err(
                        "refusing to redirect the spritesmith API token to another host".into(),
                    );
                }
            }
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .ok_or("generation timeout")?;
            let mut request = client
                .request(method.clone(), url.clone())
                .timeout(remaining)
                .header(ACCEPT, "image/png, image/svg+xml, application/json");
            if !body.is_empty() {
                request = request
                    .header(CONTENT_TYPE, content_type)
                    .body(body.to_vec());
            }
            if !operation.is_empty() {
                request = request.header("Idempotency-Key", operation);
            }
            if !credential.value.is_empty() {
                request = request.bearer_auth(&credential.value);
            }
            let response = request.send().map_err(|error| error.without_url())?;
            if !matches!(response.status().as_u16(), 301..=303 | 307 | 308) {
                return Ok(response);
            }
            let Some(location) = response.headers().get(LOCATION) else {
                return Ok(response);
            };
            if hop == 10 {
                return Err("stopped after 10 redirects".into());
            }
            url = url.join(location.to_str()?)?;
            if matches!(response.status().as_u16(), 301..=303) && method != Method::HEAD {
                method = Method::GET;
                body = &[];
            }
        }
        unreachable!()
    }
}

fn json_body(response: Response) -> Result<Value> {
    let mut bytes = Vec::new();
    response.take(4097).read_to_end(&mut bytes)?;
    if bytes.len() > 4096 {
        return Err("API returned oversized generation state".into());
    }
    Ok(serde_json::from_slice(&bytes)?)
}

fn await_generation(
    client: &Client,
    response: Response,
    request: &Request,
    credential: &Credential,
    deadline: Instant,
) -> Result<Response> {
    let job = json_body(response)?;
    let id = job["id"]
        .as_str()
        .filter(|id| {
            id.len() == 32
                && id
                    .bytes()
                    .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
        })
        .ok_or("API returned an invalid generation identity")?;
    let endpoint = format!("{}/v1/generations/{id}", request.api.trim_end_matches('/'));
    loop {
        let response = Session {
            client,
            credential,
            deadline,
        }
        .send(Method::GET, &endpoint, &[], "", "")
        .map_err(|error| format!("generation {id} remains recoverable: {error}"))?;
        if response.status() != StatusCode::OK {
            return Err(format!(
                "generation {id} status unavailable ({})",
                response.status().as_u16()
            )
            .into());
        }
        match json_body(response)?["status"].as_str().unwrap_or("") {
            "succeeded" => {
                return Session {
                    client,
                    credential,
                    deadline,
                }
                .send(
                    Method::GET,
                    &format!("{endpoint}/result?format={}", request.format),
                    &[],
                    "",
                    "",
                );
            }
            "uncertain" => {
                return Err(format!(
                    "generation {id} needs reconciliation; no paid resubmission was attempted"
                )
                .into());
            }
            "failed" => return Err(format!("generation {id} failed").into()),
            "receiving" | "queued" | "claimed" | "submitting" | "processing" => {
                let remaining = deadline
                    .checked_duration_since(Instant::now())
                    .ok_or_else(|| format!("generation {id} remains recoverable: timeout"))?;
                std::thread::sleep(remaining.min(Duration::from_millis(500)));
            }
            _ => return Err("API returned an unknown generation state".into()),
        }
    }
}

pub fn process(request: &Request, credential: &Credential) -> Result<()> {
    let model = match request.model.as_str() {
        "" | "haiku" | "anthropic/claude-haiku-5.5" => "anthropic/claude-haiku-5.5",
        "opus" | "anthropic/claude-opus-5.5" => "anthropic/claude-opus-5.5",
        _ => return Err("model must be haiku or opus".into()),
    };
    if request.kind != "vector" || !matches!(request.format.as_str(), "png" | "svg") {
        return Err("choose vector/png or vector/svg".into());
    }
    let url = Url::parse(&request.api)?;
    if !credential.value.is_empty() {
        check_transport(&url)?;
    }
    let path = Path::new(&request.out);
    check_output(path)?;
    if request.prompt.trim().is_empty() {
        return Err("prompt is required".into());
    }
    let operation = if request.operation.is_empty() {
        uuid::Uuid::new_v4().simple().to_string()
    } else {
        request.operation.clone()
    };
    if operation.len() > 128
        || !operation
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || c == b'-' || c == b'_')
    {
        return Err(
            "invalid operation ID; use at most 128 letters, numbers, underscores or hyphens".into(),
        );
    }
    let boundary = format!("spritesmith-{}", uuid::Uuid::new_v4().simple());
    let mut body = String::new();
    for (name, value) in [
        ("kind", request.kind.as_str()),
        ("workflow", "agent"),
        ("subject", "character"),
        ("agent_model", model),
        ("prompt", request.prompt.as_str()),
    ] {
        body.push_str(&format!(
            "--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n"
        ));
    }
    body.push_str(&format!("--{boundary}--\r\n"));
    let deadline = Instant::now() + Duration::from_secs(600);
    let _ = rustls::crypto::ring::default_provider().install_default();
    let client = Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .retry(reqwest::retry::never())
        .build()?;
    let response=Session {client:&client,credential,deadline}.send(Method::POST,&format!("{}/v1/prompt",request.api.trim_end_matches('/')),body.as_bytes(),&format!("multipart/form-data; boundary={boundary}"),&operation).map_err(|error|format!("generation submission interrupted; retain --operation-id {operation} for a safe retry: {error}"))?;
    let mut response = if response.status() == StatusCode::ACCEPTED {
        await_generation(&client, response, request, credential, deadline)?
    } else {
        response
    };
    if response.status() == StatusCode::UNAUTHORIZED {
        return Err(if credential.value.is_empty() {format!("unauthorized: this API requires a spritesmith API token and none is configured.\n  Create a Clerk API key for your spritesmith account, then export {}=<token>\n  or write the token to {} and run: chmod 600 {}",crate::token::ENV,crate::token::hint(),crate::token::hint())}else{format!("unauthorized: the API rejected the spritesmith API token from {}.\n  It may have been revoked, or it may have expired. Replace it and try again.",credential.source)}.into());
    }
    if !response.status().is_success() {
        let status = response.status();
        let mut data = String::new();
        response.take(65537).read_to_string(&mut data)?;
        let message = serde_json::from_str::<Value>(&data)
            .ok()
            .and_then(|v| v["error"].as_str().map(str::to_owned))
            .filter(|s| !s.is_empty())
            .unwrap_or_else(|| data.trim().to_owned());
        return Err(if message.is_empty() {
            format!("API returned {status}")
        } else {
            message
        }
        .into());
    }
    let expected = if request.format == "svg" {
        "image/svg+xml"
    } else {
        "image/png"
    };
    if response
        .headers()
        .get(CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.split(';').next())
        .map(str::trim)
        != Some(expected)
    {
        return Err(
            "the API answered with content type that is not the sprite format requested".into(),
        );
    }
    write_output(path, &mut response)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn credential_transport_handles_tls_loopback_and_plaintext_downgrades() {
        for allowed in [
            "https://api.example.invalid",
            "http://localhost:8080",
            "http://127.0.0.1:8080",
            "http://[::1]:8080",
        ] {
            assert!(check_transport(&Url::parse(allowed).unwrap()).is_ok());
        }
        for refused in [
            "http://api.example.invalid",
            "http://127.0.0.1.example.invalid",
            "ftp://localhost",
            "http://192.168.1.2",
        ] {
            assert!(check_transport(&Url::parse(refused).unwrap()).is_err());
        }
    }
}

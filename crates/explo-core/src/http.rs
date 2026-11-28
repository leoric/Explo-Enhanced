//! Small blocking HTTP helpers with retries.

use anyhow::{anyhow, bail, Context, Result};
use reqwest::blocking::{Client, RequestBuilder, Response};
use serde::de::DeserializeOwned;
use std::time::Duration;

pub const USER_AGENT: &str = concat!("Explo-RS/", env!("CARGO_PKG_VERSION"), " ( https://github.com/leoric/ExploEnhanced )");

pub fn client(timeout_secs: u64) -> Client {
    Client::builder()
        .user_agent(USER_AGENT)
        .timeout(Duration::from_secs(timeout_secs))
        .build()
        .expect("HTTP client")
}

/// Sends the request built by `build`, retrying on network errors, 429 and 5xx.
pub fn send_with_retry(build: impl Fn() -> RequestBuilder) -> Result<Response> {
    let mut last: Option<anyhow::Error> = None;
    for attempt in 0..3u64 {
        if attempt > 0 {
            std::thread::sleep(Duration::from_secs(2 + attempt * 3));
        }
        match build().send() {
            Ok(resp) => {
                let status = resp.status();
                if status.is_success() {
                    return Ok(resp);
                }
                if status.as_u16() == 429 {
                    let wait = resp.headers().get("retry-after").and_then(|v| v.to_str().ok()).and_then(|v| v.parse::<u64>().ok()).unwrap_or(5);
                    std::thread::sleep(Duration::from_secs(wait.min(30)));
                }
                let url = resp.url().to_string();
                let transient = status.is_server_error() || status.as_u16() == 429;
                let err = anyhow!("HTTP {} from {}", status.as_u16(), redact(&url));
                if !transient {
                    return Err(err);
                }
                last = Some(err);
            }
            Err(e) => last = Some(anyhow!(e.without_url()).context("request failed")),
        }
    }
    Err(last.unwrap_or_else(|| anyhow!("request failed")))
}

/// One attempt, success status required. For lookups that have a fallback anyway.
pub fn send_once(rb: RequestBuilder) -> Result<Response> {
    let resp = rb.send().map_err(|e| anyhow!(e.without_url()).context("request failed"))?;
    if !resp.status().is_success() {
        bail!("HTTP {} from {}", resp.status().as_u16(), redact(resp.url().as_str()));
    }
    Ok(resp)
}

pub fn get_json<T: DeserializeOwned>(client: &Client, url: &str, headers: &[(&str, &str)]) -> Result<T> {
    let resp = send_with_retry(|| {
        let mut rb = client.get(url).header("Accept", "application/json");
        for (k, v) in headers {
            rb = rb.header(*k, *v);
        }
        rb
    })?;
    let body = resp.bytes().context("reading response")?;
    if body.is_empty() {
        bail!("empty response from {}", redact(url));
    }
    serde_json::from_slice(&body).with_context(|| format!("unexpected response from {}", redact(url)))
}

/// Strips credentials that may sit in query strings (Subsonic `t=`/`s=`/`u=`).
pub fn redact(url: &str) -> String {
    match url.split_once('?') {
        Some((base, _)) => format!("{base}?…"),
        None => url.to_string(),
    }
}

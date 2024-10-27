// src/contract.rs
use cosmwasm_std::{
    entry_point, to_json_binary, Binary, DepsMut, Env, MessageInfo, Response, StdResult,
};

use crate::msg::{ExecuteMsg, InstantiateMsg, StoreFileResponse};
use crate::state::FILES;
use data_encoding::BASE32;
use sha2::{Digest, Sha256};
use zstd::encode_all;

#[entry_point]
pub fn instantiate(
    _deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    _msg: InstantiateMsg,
) -> StdResult<Response> {
    // Initialization logic if necessary
    Ok(Response::default())
}

#[entry_point]
pub fn execute(
    deps: DepsMut,
    _env: Env,
    _info: MessageInfo,
    msg: ExecuteMsg,
) -> StdResult<Response> {
    match msg {
        ExecuteMsg::StoreFile { data } => execute_store_file(deps, data),
    }
}

pub fn execute_store_file(deps: DepsMut, data: Binary) -> StdResult<Response> {
    // Check data size
    let max_size = 999 * 1024; // 999 KB
    if data.len() > max_size {
        return Err(cosmwasm_std::StdError::generic_err(
            "File size exceeds 999 KB limit",
        ));
    }

    // Compress the data
    let compressed_data = encode_all(data.as_slice(), 3).map_err(|err| {
        cosmwasm_std::StdError::generic_err(format!("Compression error: {}", err))
    })?; // Compression level 3

    // Compute SHA256 hash of the compressed data
    let mut hasher = Sha256::new();
    hasher.update(&compressed_data);
    let digest = hasher.finalize();

    let sha256_hex = hex::encode(digest);

    // Compute CID
    let cid_bytes = compute_cid(&digest);

    // Base32 encode CID bytes
    let cid_base32 = BASE32.encode(&cid_bytes);

    // CID is prefixed with "b" in CIDv1 Base32 encoding
    let cid_string = format!("b{}", cid_base32.to_lowercase());

    // Store compressed file data in storage, keyed by SHA256 hash
    // Convert compressed_data to Binary and save it
    FILES.save(deps.storage, &sha256_hex, &compressed_data.clone().into())?;

    let res = Response::new()
        .add_attribute("method", "execute_store_file")
        .add_attribute("original_size", data.len().to_string())
        .add_attribute("compressed_size", compressed_data.len().to_string())
        .set_data(to_json_binary(&StoreFileResponse {
            sha256: sha256_hex,
            cid: cid_string,
        })?);

    Ok(res)
}

fn compute_cid(digest: &[u8]) -> Vec<u8> {
    // Multihash prefix for SHA2-256
    // [0x12][0x20][digest]
    let mut multihash = Vec::with_capacity(2 + digest.len());
    multihash.push(0x12); // SHA2-256 code
    multihash.push(0x20); // Digest length in bytes (32 bytes)
    multihash.extend_from_slice(digest);

    // CID prefix
    // [0x01][0x55][multihash]
    let mut cid = Vec::with_capacity(2 + multihash.len());
    cid.push(0x01); // CIDv1
    cid.push(0x55); // Raw binary data codec
    cid.extend_from_slice(&multihash);

    cid
}

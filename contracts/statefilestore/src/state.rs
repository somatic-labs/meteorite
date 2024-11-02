// src/state.rs
use cosmwasm_std::Binary;
use cw_storage_plus::Map;

// Map from SHA256 hash to file data
pub const FILES: Map<&str, Binary> = Map::new("files");

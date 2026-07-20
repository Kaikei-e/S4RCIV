// In-browser integrity verifier for one Stream (事案) — ADR-000014.
//
// It recomputes, on the READER'S OWN machine, the canonical log hash of every
// event from its HashableEvent fields and compares it to the stored hash, then
// checks the per-stream content chain. This is deliberately NOT a trust badge
// from S4RCIV: §5's adversary distrusts S4RCIV, so the value is that the reader
// reproduces the numbers themselves rather than taking a server ✓ on faith.
//
// Byte-identity with the Go collector's
//   log_hash = sha256(proto.MarshalOptions{Deterministic:true}.Marshal(HashableEvent))
// is NOT guaranteed by protobuf's Deterministic option (it is explicitly not a
// canonical/portable spec). It holds only for this scalar-only, all-fields-
// populated schema, and is PINNED by the CDC golden-vector test
// (verifier.cdc.test.ts) against vectors produced by the Go side. If that test
// is green, this verifier agrees with Go byte-for-byte.
//
// SCOPE (honest bounds): the log chain is GLOBAL — every stream's events are
// interleaved by seq — so a single-stream export cannot check global log_prev_hash
// linkage; that is delegated to a third-party full mirror via export. What runs
// here is the bounded check of ADR-000014 §3: (1) each log_hash recomputes,
// (2) the stream's content chain is continuous, (3) coverage by a checkpoint.

import { fromJson, toBinary } from '@bufbuild/protobuf';
import {
	HashableEventSchema,
	type HashableEvent
} from '$lib/gen/s4rciv/observation/v1/observation_pb';

const GENESIS_LOG_PREV = '0'.repeat(64);

// ── Wire shape of the GetStreamVerification response, as the SvelteKit load hands
// it to the browser (proto3 JSON via toJson: camelCase, int64 → string,
// enum → name). `hashable` is the HashableEvent JSON; we re-parse it through the
// generated schema so the bytes we re-marshal are exactly the canonical form. ──

export interface VerifiableEventJson {
	seq: string;
	hashable: unknown; // HashableEvent proto-JSON; validated via fromJson(HashableEventSchema, …)
	logHash: string; // stored log_hash, lowercase hex
}

export interface VerificationCheckpointJson {
	throughSeq?: string;
	treeSize?: string;
	rootHash?: string;
	algVersion?: string;
	signed?: boolean;
	signerKeyId?: string;
	recordedAt?: string;
}

export interface StreamVerificationJson {
	streamId?: string;
	source?: string;
	algVersion?: string;
	events?: VerifiableEventJson[];
	hasCheckpoint?: boolean;
	checkpoint?: VerificationCheckpointJson;
}

// ── Result of running the verifier (consumed by the panel) ──────────────────────

export interface EventVerification {
	seq: string;
	storedLogHash: string;
	recomputedLogHash: string;
	logHashOk: boolean;
	// Content-chain link to the previous content-bearing event in this stream.
	// null when not applicable (this event carries no snapshot, e.g. vanished, or
	// it is the first content-bearing event so prev_content_hash is "").
	contentLinkOk: boolean | null;
}

export interface CheckpointStatus {
	present: boolean;
	signed: boolean; // false in v0 — no signing job yet (ADR-000014 §4 deferred)
	throughSeq: string | null;
}

export interface StreamVerificationResult {
	streamId: string;
	algVersion: string;
	events: EventVerification[];
	allLogHashesOk: boolean;
	contentChainOk: boolean;
	checkpoint: CheckpointStatus;
}

/** Lowercase hex of an ArrayBuffer / byte array. */
export function toHex(bytes: ArrayBuffer | Uint8Array): string {
	const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
	let out = '';
	for (const b of view) out += b.toString(16).padStart(2, '0');
	return out;
}

/** sha256(bytes) as lowercase hex, via the browser's WebCrypto (secure context). */
export async function sha256Hex(bytes: Uint8Array): Promise<string> {
	// Copy into a fresh ArrayBuffer-backed view: toBinary() may be typed
	// Uint8Array<ArrayBufferLike>, which lib.dom's BufferSource rejects (it forbids
	// SharedArrayBuffer). The copy is negligible — these messages are tiny.
	const digest = await crypto.subtle.digest('SHA-256', new Uint8Array(bytes));
	return toHex(digest);
}

/**
 * Canonical bytes of a HashableEvent: the exact Deterministic-marshal the Go
 * collector hashed. protobuf-es emits proto3 fields in field-number order and
 * omits zero/empty values — identical to Go for this scalar-only schema.
 */
export function hashableBytes(he: HashableEvent): Uint8Array {
	return toBinary(HashableEventSchema, he);
}

/** Recompute log_hash from a HashableEvent's proto-JSON (the RPC payload form). */
export async function recomputeLogHash(hashableJson: unknown): Promise<string> {
	const he = fromJson(HashableEventSchema, hashableJson as never);
	return sha256Hex(hashableBytes(he));
}

/**
 * Run the bounded in-browser verification for one stream's export.
 * Events must arrive in stream_seq order (the RPC guarantees this).
 */
export async function verifyStream(
	payload: StreamVerificationJson
): Promise<StreamVerificationResult> {
	const events = payload.events ?? [];
	const out: EventVerification[] = [];
	let contentChainOk = true;
	// content_hash of the most recent content-bearing event seen so far ("" before
	// the first one), to validate the next event's prev_content_hash.
	let lastContentHash = '';

	for (const ev of events) {
		const he = fromJson(HashableEventSchema, ev.hashable as never);
		const recomputed = await sha256Hex(hashableBytes(he));
		const logHashOk = recomputed === ev.logHash;

		let contentLinkOk: boolean | null = null;
		if (he.contentHash !== '') {
			// This event carries a snapshot: its prev_content_hash must point at the
			// previous content-bearing event's content_hash ("" for the first).
			contentLinkOk = he.prevContentHash === lastContentHash;
			if (!contentLinkOk) contentChainOk = false;
			lastContentHash = he.contentHash;
		}

		out.push({
			seq: ev.seq,
			storedLogHash: ev.logHash,
			recomputedLogHash: recomputed,
			logHashOk,
			contentLinkOk
		});
	}

	const cp = payload.checkpoint;
	return {
		streamId: payload.streamId ?? '',
		algVersion: payload.algVersion ?? '',
		events: out,
		allLogHashesOk: out.length > 0 && out.every((e) => e.logHashOk),
		contentChainOk,
		checkpoint: {
			present: payload.hasCheckpoint === true,
			signed: cp?.signed === true,
			throughSeq: cp?.throughSeq ?? null
		}
	};
}

// Re-export for callers that want to assert the genesis sentinel explicitly.
export { GENESIS_LOG_PREV };

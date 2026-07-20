import { describe, it, expect } from 'vitest';
import { toJson } from '@bufbuild/protobuf';
import { HashableEventSchema } from '$lib/gen/s4rciv/observation/v1/observation_pb';
import { loadGolden, buildHashable, type GoldenVector } from './golden.fixture';
import { verifyStream, type VerifiableEventJson, type StreamVerificationJson } from './verifier';

// Build a GetStreamVerification payload (as the SvelteKit load hands it over) from
// golden vectors: hashable as proto-JSON, logHash = the Go-produced hash.
function event(v: GoldenVector): VerifiableEventJson {
	return {
		seq: v.fields.streamSeq,
		hashable: toJson(HashableEventSchema, buildHashable(v.fields)),
		logHash: v.logHashHex
	};
}

function payload(vectors: GoldenVector[]): StreamVerificationJson {
	return {
		streamId: 'kokkai:100000000X00120260101',
		source: 'kokkai',
		algVersion: 'proto-linked-v1',
		events: vectors.map(event),
		hasCheckpoint: false
	};
}

const golden = loadGolden();
const byName = (n: string): GoldenVector => {
	const v = golden.vectors.find((x) => x.name === n);
	if (!v) throw new Error(`golden vector ${n} missing`);
	return v;
};
// The 4-event chain (genesis → changed → vanished → restored).
const chain = [
	byName('genesis_observed'),
	byName('second_changed'),
	byName('third_vanished'),
	byName('fourth_restored')
];

describe('verifyStream', () => {
	it('accepts an untampered chain: every log_hash recomputes, content chain continuous', async () => {
		const r = await verifyStream(payload(chain));
		expect(r.allLogHashesOk).toBe(true);
		expect(r.contentChainOk).toBe(true);
		expect(r.events).toHaveLength(4);
		expect(r.events.every((e) => e.logHashOk)).toBe(true);
		// the vanished event carries no snapshot → content link not applicable
		const vanished = r.events[2];
		expect(vanished?.contentLinkOk).toBeNull();
	});

	it('reports checkpoint absent + unsigned in v0', async () => {
		const r = await verifyStream(payload(chain));
		expect(r.checkpoint.present).toBe(false);
		expect(r.checkpoint.signed).toBe(false);
		expect(r.checkpoint.throughSeq).toBeNull();
	});

	it('flags a tampered stored log_hash (recompute disagrees)', async () => {
		const p = payload(chain);
		const first = p.events?.[0];
		if (!first) throw new Error('no events');
		first.logHash = 'deadbeef'.repeat(8); // 64 hex chars, wrong value
		const r = await verifyStream(p);
		expect(r.allLogHashesOk).toBe(false);
		expect(r.events[0]?.logHashOk).toBe(false);
		expect(r.events[1]?.logHashOk).toBe(true); // others untouched
	});

	it('flags a broken content chain when a content-bearing event is missing', async () => {
		// Drop the middle "changed" event: the "restored" event's prev_content_hash
		// then points at a snapshot no longer present in the stream.
		const gap = [chain[0], chain[2], chain[3]].filter(
			(v): v is GoldenVector => v !== undefined
		);
		const r = await verifyStream(payload(gap));
		expect(r.allLogHashesOk).toBe(true); // each event's own hash still recomputes
		expect(r.contentChainOk).toBe(false); // but the snapshot chain has a gap
	});

	it('treats an empty stream as not-all-ok (nothing to attest)', async () => {
		const r = await verifyStream(payload([]));
		expect(r.allLogHashesOk).toBe(false);
		expect(r.events).toHaveLength(0);
	});
});

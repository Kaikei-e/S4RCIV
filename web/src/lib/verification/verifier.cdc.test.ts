// Consumer-Driven Contract: the in-browser verifier MUST reproduce, byte-for-byte,
// the canonical wire bytes and log_hash that the Go collector produced for every
// golden HashableEvent (ADR-000014 / ADR-000003). Producer:
// services/api/internal/domain/observation/golden_test.go.
//
// If this fails, Go and the browser disagree on the canonical form — third-party
// verification is broken. The likely causes: a protobuf-es upgrade changed the
// encoder, or a map/optional/repeated field crept into HashableEvent (which the
// scalar-only invariant forbids precisely because it breaks portability).

import { describe, it, expect } from 'vitest';
import { toBinary } from '@bufbuild/protobuf';
import { HashableEventSchema } from '$lib/gen/s4rciv/observation/v1/observation_pb';
import { loadGolden, buildHashable } from './golden.fixture';
import { toHex, sha256Hex } from './verifier';

const golden = loadGolden();

describe('CDC: HashableEvent byte-identity with Go (proto-linked-v1)', () => {
	it('golden file pins the expected alg_version and is non-empty', () => {
		expect(golden.algVersion).toBe('proto-linked-v1');
		expect(golden.vectors.length).toBeGreaterThan(0);
	});

	for (const v of golden.vectors) {
		it(`reproduces Deterministic wire bytes for "${v.name}"`, () => {
			const he = buildHashable(v.fields);
			expect(toHex(toBinary(HashableEventSchema, he))).toBe(v.wireHex);
		});

		it(`reproduces log_hash for "${v.name}"`, async () => {
			const he = buildHashable(v.fields);
			expect(await sha256Hex(toBinary(HashableEventSchema, he))).toBe(v.logHashHex);
		});
	}

	it('omits zero stream_seq and UNSPECIFIED type from the wire (proto3 implicit presence)', () => {
		const zero = golden.vectors.find((v) => v.name === 'zero_stream_seq_unspecified_type');
		if (!zero) throw new Error('zero/unspecified vector missing from golden');
		expect(zero.fields.streamSeq).toBe('0');
		expect(zero.fields.type).toBe(0);
		// The definitive omission proof: rebuilding from these zero values and
		// re-marshaling reproduces the Go bytes, which themselves omitted the fields.
		expect(toHex(toBinary(HashableEventSchema, buildHashable(zero.fields)))).toBe(zero.wireHex);
	});

	it('keeps stream_seq beyond 2^53 exact (BigInt, not JS number)', () => {
		const big = golden.vectors.find((v) => v.name === 'large_stream_seq_beyond_js_safe_int');
		if (!big) throw new Error('large stream_seq vector missing from golden');
		expect(big.fields.streamSeq).toBe('9007199254740993');
		expect(toHex(toBinary(HashableEventSchema, buildHashable(big.fields)))).toBe(big.wireHex);
	});
});

// Test-only loader for the CDC golden vectors. The golden file is PRODUCED by
// the Go side (services/api/internal/domain/observation/golden_test.go) and is
// the contract artifact; this consumer imports the exact same bytes across the
// monorepo — that cross-tree reach IS the consumer-driven contract. We import the
// JSON directly (Vite/Vitest resolve it; resolveJsonModule typechecks it) so this
// stays free of node:* APIs and the app type-check needs no @types/node.

import { create } from '@bufbuild/protobuf';
import {
	HashableEventSchema,
	type HashableEvent
} from '$lib/gen/s4rciv/observation/v1/observation_pb';
import goldenJson from '../../../../services/api/internal/domain/observation/testdata/hashable_golden.json';

export interface GoldenFields {
	eventId: string;
	streamId: string;
	streamSeq: string; // int64 as decimal string
	type: number; // EventType enum number
	source: string;
	fetcherVersion: string;
	observedAt: string;
	sourcePublishedAt: string;
	contentHash: string;
	prevContentHash: string;
	logPrevHash: string;
}

export interface GoldenVector {
	name: string;
	fields: GoldenFields;
	wireHex: string;
	logHashHex: string;
}

export interface GoldenFile {
	algVersion: string;
	note: string;
	vectors: GoldenVector[];
}

export function loadGolden(): GoldenFile {
	// The imported JSON is typed as its literal shape; it satisfies GoldenFile
	// structurally (this is the CDC contract boundary).
	return goldenJson as GoldenFile;
}

/**
 * Build a HashableEvent from golden field values. stream_seq goes through
 * BigInt so values beyond 2^53 stay exact (a plain JS number would silently
 * round — the trap the research flagged).
 */
export function buildHashable(f: GoldenFields): HashableEvent {
	return create(HashableEventSchema, {
		eventId: f.eventId,
		streamId: f.streamId,
		streamSeq: BigInt(f.streamSeq),
		type: f.type,
		source: f.source,
		fetcherVersion: f.fetcherVersion,
		observedAt: f.observedAt,
		sourcePublishedAt: f.sourcePublishedAt,
		contentHash: f.contentHash,
		prevContentHash: f.prevContentHash,
		logPrevHash: f.logPrevHash
	});
}

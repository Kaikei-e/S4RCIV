// Read-only access to the Connect-RPC QueryService, server-side only (SSR/BFF;
// D1): the browser never touches the API, which stays private on the compose
// network. This is the D2 contract — the buf-generated Connect client over a
// connect-node transport, with the proto as the single source of truth.
//
// Each call returns the response as proto3 JSON via toJson(): int64 → string,
// lowerCamelCase keys — exactly the $lib/types shape, and a plain serializable
// object that survives the SvelteKit load boundary (a proto Message would not).

import { env } from '$env/dynamic/private';
import { createClient } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-node';
import { toJson, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import {
	QueryService,
	ListTimelineResponseSchema,
	GetMeetingResponseSchema,
	GetLawResponseSchema,
	GetLawChangesResponseSchema,
	ListLegislatorVotesResponseSchema,
	GetVoteEventResponseSchema,
	ListVoteEventsResponseSchema,
	ListSangiinVoteEventsResponseSchema,
	GetSangiinVoteMapResponseSchema,
	GetStreamVerificationResponseSchema,
	GetMastheadStatusResponseSchema,
	ListCheckpointsResponseSchema
} from '$lib/gen/s4rciv/query/v1/query_pb';
import type {
	ListTimelineRequest,
	ListTimelineResponse,
	GetLawResponse,
	GetLawChangesResponse,
	GetMeetingResponse,
	ListLegislatorVotesResponse,
	GetVoteEventResponse,
	ListVoteEventsResponse,
	ListSangiinVoteEventsResponse,
	GetSangiinVoteMapResponse,
	MastheadStatus,
	ListCheckpointsResponse
} from '$lib/types';
// Type-only: the verifier owns the GetStreamVerification JSON shape so the panel
// and this client agree on one definition. import type is erased — no runtime
// (browser-only WebCrypto) code is pulled into the server bundle.
import type { StreamVerificationJson } from '$lib/verification/verifier';

const BASE = (env.API_URL ?? 'http://127.0.0.1:8080').replace(/\/$/, '');

const client = createClient(
	QueryService,
	// defaultTimeoutMs: a hung upstream must fail the SSR request quickly instead of
	// pinning a Node worker open indefinitely (request-smuggled slowloris resilience).
	createConnectTransport({ baseUrl: BASE, httpVersion: '1.1', defaultTimeoutMs: 10_000 })
);

function json<Desc extends DescMessage>(schema: Desc, m: MessageShape<Desc>): unknown {
	return toJson(schema, m, { emitDefaultValues: true });
}

export async function listTimeline(req: ListTimelineRequest): Promise<ListTimelineResponse> {
	return json(ListTimelineResponseSchema, await client.listTimeline(req)) as ListTimelineResponse;
}

export async function getMeeting(issueId: string): Promise<GetMeetingResponse> {
	return json(GetMeetingResponseSchema, await client.getMeeting({ issueId })) as GetMeetingResponse;
}

export async function getLaw(lawId: string): Promise<GetLawResponse> {
	return json(GetLawResponseSchema, await client.getLaw({ lawId })) as GetLawResponse;
}

export async function getLawChanges(lawId: string): Promise<GetLawChangesResponse> {
	return json(
		GetLawChangesResponseSchema,
		await client.getLawChanges({ lawId, pageSize: 50 })
	) as GetLawChangesResponse;
}

export async function listLegislatorVotes(
	personId: string
): Promise<ListLegislatorVotesResponse> {
	return json(
		ListLegislatorVotesResponseSchema,
		await client.listLegislatorVotes({ personId, pageSize: 100 })
	) as ListLegislatorVotesResponse;
}

export async function getVoteEvent(voteEventId: string): Promise<GetVoteEventResponse> {
	return json(
		GetVoteEventResponseSchema,
		await client.getVoteEvent({ voteEventId })
	) as GetVoteEventResponse;
}

// 現会期 (session 0 = latest) の記名投票だけを地図セレクタ用に返す (ADR-000008).
export async function listVoteEvents(session = 0): Promise<ListVoteEventsResponse> {
	return json(
		ListVoteEventsResponseSchema,
		await client.listVoteEvents({ session, mappableOnly: true, pageSize: 100 })
	) as ListVoteEventsResponse;
}

// 参議院本会議投票結果 (ADR-000010).
export async function listSangiinVoteEvents(session = 0): Promise<ListSangiinVoteEventsResponse> {
	return json(
		ListSangiinVoteEventsResponseSchema,
		await client.listSangiinVoteEvents({ session, pageSize: 100 })
	) as ListSangiinVoteEventsResponse;
}

export async function getSangiinVoteMap(voteEventId: string): Promise<GetSangiinVoteMapResponse> {
	return json(
		GetSangiinVoteMapResponseSchema,
		await client.getSangiinVoteMap({ voteEventId })
	) as GetSangiinVoteMapResponse;
}

// 完全性検証 read surface (ADR-000014): one Stream's events + covering checkpoint,
// for the in-browser verifier. emitDefaultValues keeps the zero/empty HashableEvent
// fields present in the JSON so the verifier re-marshals the exact canonical form.
export async function getStreamVerification(streamId: string): Promise<StreamVerificationJson> {
	return json(
		GetStreamVerificationResponseSchema,
		await client.getStreamVerification({ streamId })
	) as StreamVerificationJson;
}

// Global provenance for the masthead (ADR-000018/000019): watch coverage + the latest
// signed checkpoint, if one exists.
export async function getMastheadStatus(): Promise<MastheadStatus> {
	return json(GetMastheadStatusResponseSchema, await client.getMastheadStatus({})) as MastheadStatus;
}

// The signed checkpoint feed (ADR-000019), newest first, for passive exposure.
export async function listCheckpoints(limit = 200): Promise<ListCheckpointsResponse> {
	return json(
		ListCheckpointsResponseSchema,
		await client.listCheckpoints({ limit })
	) as ListCheckpointsResponse;
}

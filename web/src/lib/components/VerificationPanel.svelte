<script lang="ts">
	import {
		verifyStream,
		type StreamVerificationJson,
		type StreamVerificationResult
	} from '$lib/verification/verifier';
	import { fromJson } from '@bufbuild/protobuf';
	import { HashableEventSchema } from '$lib/gen/s4rciv/observation/v1/observation_pb';
	import { toJstMinute } from '$lib/time';

	interface Props {
		// The GetStreamVerification payload from the SvelteKit load (ADR-000014).
		data: StreamVerificationJson;
	}
	const { data }: Props = $props();

	// OPT-IN (ADR-000014 §5 / 利用規約「端末内検証」): nothing is computed until the
	// reader presses the button. The recompute runs only on THIS device, only on
	// demand — no hashing work happens on mount, so opening the page never spends the
	// visitor's CPU without an explicit action. This is the legal-cleanest posture
	// (the click IS the consent; the panel discloses what runs) and also the honest
	// one — we never show ✓ before the reader has made their own machine reproduce it.
	type Phase = 'idle' | 'running' | 'done';
	let phase = $state<Phase>('idle');
	let result = $state<StreamVerificationResult | null>(null);
	let failed = $state(false);
	// How many spine nodes the walk has fully stepped through (display order).
	let revealed = $state(0);

	// event_type (HashableEvent.type enum) → state node (color + glyph + label), per
	// DESIGN_LANGUAGE §3.3. Color encodes STATE only, never a value judgment (§1/§5-C).
	// Keyed by the numeric EventType the generated message yields.
	type Status = { glyph: string; label: string; tone: string };
	const STATUS: Record<number, Status> = {
		1: { glyph: '◉', label: '観測', tone: 'info' }, // RESOURCE_OBSERVED
		2: { glyph: 'Δ', label: '変化', tone: 'changed' }, // RESOURCE_CHANGED
		3: { glyph: '⊘', label: '消失', tone: 'critical' }, // RESOURCE_VANISHED
		4: { glyph: '●', label: '復活', tone: 'nominal' } // RESOURCE_RESTORED
	};
	const st = (t: number): Status => STATUS[t] ?? STATUS[1];

	// Static spine, derivable before any computation (no ✓ yet). Re-parse each event's
	// HashableEvent through the generated message — the same form the verifier hashes —
	// to read its type/observed_at for the marker and timestamp.
	type Node = { seq: string; type: number; observedAt: string; logHash: string };
	const nodes = $derived<Node[]>(
		(data.events ?? []).map((e) => {
			let type = 1;
			let observedAt = '';
			try {
				const he = fromJson(HashableEventSchema, e.hashable as never);
				type = he.type;
				observedAt = he.observedAt;
			} catch {
				/* malformed payload: degrade to a neutral marker rather than crash the page */
			}
			return { seq: e.seq, type, observedAt, logHash: e.logHash };
		})
	);
	// Newest first, matching the design's「記録の連なり（新しい順）」. Events arrive in
	// stream_seq ASC from the RPC; reversing is purely visual (verdicts are by seq).
	const chain = $derived([...nodes].reverse());

	const verdict = $derived(new Map((result?.events ?? []).map((e) => [e.seq, e])));
	const allOk = $derived(!!result && result.allLogHashesOk && result.contentChainOk);
	const okCount = $derived(result ? result.events.filter((e) => e.logHashOk).length : 0);
	const shortHex = (h: string) => (h.length > 16 ? `${h.slice(0, 8)}…${h.slice(-6)}` : h);

	async function run() {
		if (phase === 'running') return;
		phase = 'running';
		failed = false;
		result = null;
		revealed = 0;
		try {
			// The whole stream recomputes up-front (it is fast); the walk below only
			// reveals each node's verdict in turn so the reader watches their own
			// device step through the chain.
			const r = await verifyStream(data);
			result = r;
			const reduce =
				typeof window !== 'undefined' &&
				window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
			if (reduce || r.events.length === 0) {
				revealed = r.events.length;
				phase = 'done';
				return;
			}
			for (let i = 1; i <= r.events.length; i++) {
				await new Promise((res) => setTimeout(res, 320));
				revealed = i;
			}
			phase = 'done';
		} catch {
			failed = true;
			phase = 'idle';
		}
	}
</script>

<section class="verify" aria-label="記録の確かめ">
	<div class="vhead">
		<div>
			<h2 class="label">この記録を確かめる</h2>
			<p class="lede">
				S4RCIV が記録した内容から、改ざん検知用の値を <strong>お使いのブラウザで計算し直し</strong>、
				記録済みの値と一致するか確かめます。計算は <strong>下のボタンを押したときだけ</strong>、この端末で実行されます（暗号通貨の採掘や追跡ではありません）。
				S4RCIV の「確認済み」表示を信じる必要はありません — 数字を再現するのはあなたの端末です。
			</p>
		</div>
		{#if nodes.length > 0}
			<button class="run" onclick={run} disabled={phase === 'running'} aria-live="polite">
				{phase === 'running' ? '計算中…' : phase === 'done' ? '↻ もう一度計算' : '▸ この端末で検証する'}
			</button>
		{/if}
	</div>

	{#if failed}
		<p class="status warn">確かめを実行できませんでした。時間をおいて再度お試しください。</p>
	{:else if nodes.length === 0}
		<p class="status">この事案にはまだ記録がありません。</p>
	{:else}
		<ol class="chain" aria-label="記録の連なり（新しい順）">
			{#each chain as n, i (n.seq)}
				{@const s = st(n.type)}
				{@const v = verdict.get(n.seq)}
				{@const shown = phase === 'done' || (phase === 'running' && i < revealed)}
				{@const checking = phase === 'running' && i === revealed}
				<li
					id="verify-{n.seq}"
					class="node tone-{s.tone}"
					class:shown
					class:checking
				>
					<div class="marker" aria-hidden="true">
						<span class="glyph">{s.glyph}</span>
						{#if i < chain.length - 1}<span class="link"></span>{/if}
					</div>
					<div class="body">
						<div class="row1">
							<span class="seq mono">記録 #{n.seq}</span>
							<span class="slabel">{s.label}</span>
							{#if n.observedAt}<span class="time mono">{toJstMinute(n.observedAt)} JST</span>{/if}
						</div>
						<div class="row2">
							<span class="label">改ざん検知用の値</span>
							<code class="hash mono">{shortHex(n.logHash)}</code>
							{#if shown && v}
								{#if v.logHashOk}
									<span class="mark ok mono">✓ 一致</span>
								{:else}
									<span class="mark ng mono">✗ 不一致</span>
								{/if}
								{#if v.contentLinkOk === false}
									<span class="mark ng">✗ 前の記録とのつながりが切れています</span>
								{/if}
							{:else if checking}
								<span class="mark checking mono">計算中…</span>
							{:else}
								<span class="mark idle mono">（未計算）</span>
							{/if}
						</div>
					</div>
				</li>
			{/each}
		</ol>

		{#if phase === 'done' && result}
			{#if allOk}
				<div class="result ok" role="status">
					<span class="rglyph mono" aria-hidden="true">✓</span>
					<div>
						<p class="rtitle">
							この {result.events.length} 件すべてを、記録どおりにこの端末で再現できました。
						</p>
						<p class="rsub">
							各記録の改ざん検知用の値を計算し直し、前の記録とのつながりが保たれているか確かめました。
						</p>
					</div>
				</div>
			{:else}
				<div class="result warn" role="status">
					<span class="rglyph mono" aria-hidden="true">⚠</span>
					<div>
						<p class="rtitle">
							一致しない箇所があります（再計算が一致したのは {okCount} / {result.events.length} 件）。
						</p>
						<p class="rsub">上の連なりで ✗ の付いた記録をご確認ください。</p>
					</div>
				</div>
			{/if}
		{/if}

		<div class="panels">
			<div class="panel">
				<h3 class="label">チェックポイント</h3>
				{#if !data.hasCheckpoint || !data.checkpoint}
					<p class="cp-empty">
						この事案を覆うチェックポイントはまだありません（連鎖内の整合のみ確認します）。
					</p>
				{:else}
					{@const cp = data.checkpoint}
					<dl class="cp">
						<dt>記録</dt>
						<dd class="mono">#{cp?.throughSeq ?? '—'} まで</dd>
						<dt>署名</dt>
						<dd>
							{#if cp?.signed}署名あり（{cp.signerKeyId || '鍵ID不明'}）{:else}未署名（v0・署名ジョブ未稼働）{/if}
						</dd>
						{#if cp?.rootHash}
							<dt>まとめの値</dt>
							<dd class="mono hash">{shortHex(cp.rootHash)}</dd>
						{/if}
					</dl>
				{/if}
			</div>

			<div class="panel">
				<h3 class="label">仕組み</h3>
				<ol class="howto">
					<li><span class="n mono">1</span>各記録は、前の記録から計算した値を持っています。</li>
					<li><span class="n mono">2</span>この端末で値を計算し直し、つながりが保たれているか確かめます。</li>
					<li><span class="n mono">3</span>チェックポイントが、ある時点までの記録をまとめて固定します。</li>
				</ol>
				<p class="howto-note">
					いつの時点かを外部（Internet Archive 等）に固定する仕組みは今後対応します。全件を通した計算は、公開エクスポートを使えば
					S4RCIV 以外の場所でも再現できます。
				</p>
			</div>
		</div>

		{#if result}
			<details class="tech">
				<summary>技術的な内訳</summary>
				<dl class="kv mono">
					<dt>ハッシュ方式</dt>
					<dd>{result.algVersion || '—'}</dd>
					<dt>チェックポイント</dt>
					<dd>
						{#if result.checkpoint.present}
							あり（through_seq {result.checkpoint.throughSeq}）/ 署名{result.checkpoint.signed
								? 'あり'
								: 'なし'}
						{:else}
							未生成（この事案は連鎖内整合のみ確認）
						{/if}
					</dd>
				</dl>
				<table class="hashes mono">
					<thead>
						<tr><th>#seq</th><th>再計算した log_hash</th><th>記録の log_hash</th></tr>
					</thead>
					<tbody>
						{#each result.events as ev (ev.seq)}
							<tr class:ng={!ev.logHashOk}>
								<td>{ev.seq}</td>
								<td>{shortHex(ev.recomputedLogHash)}</td>
								<td>{shortHex(ev.storedLogHash)}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</details>
		{/if}

		<p class="note">
			※ いつの時点の記録かを外部（Internet Archive 等）に固定する「外部アンカー」はまだ運用していません。これは
			S4RCIV 自身が過去をまるごと書き換えた場合に備えるもので、今後対応します。
		</p>
	{/if}
</section>

<style>
	.verify {
		margin: 28px 0;
		padding: 16px;
		background: var(--surface-1);
		border: 1px solid var(--hairline-2);
		border-radius: var(--r);
	}
	.vhead {
		display: flex;
		flex-wrap: wrap;
		align-items: flex-start;
		justify-content: space-between;
		gap: 12px;
	}
	h2.label {
		display: block;
		margin: 0 0 8px;
	}
	.lede {
		margin: 0 0 4px;
		font-size: 13px;
		line-height: 1.7;
		color: var(--text-2);
		max-width: 56ch;
	}
	.run {
		flex: none;
		font-size: 13px;
		font-weight: 600;
		padding: 8px 14px;
		border-radius: var(--r-sm);
		border: 1px solid color-mix(in srgb, var(--accent) 45%, transparent);
		background: color-mix(in srgb, var(--accent) 14%, transparent);
		color: var(--accent);
		cursor: pointer;
		white-space: nowrap;
	}
	.run:hover:not(:disabled) {
		background: color-mix(in srgb, var(--accent) 22%, transparent);
	}
	.run:disabled {
		opacity: 0.6;
		cursor: default;
	}
	.status {
		font-size: 14px;
		margin: 12px 0;
		color: var(--text-2);
	}
	.status.warn {
		color: var(--st-caution-t);
	}

	/* ── Connected chain spine (記録の連なり) — a colour+glyph marker per node with a
	   vertical link line joining it to the next, so the hash chain reads as one
	   continuous spine rather than a flat list. ── */
	.chain {
		list-style: none;
		margin: 16px 0 0;
		padding: 0;
	}
	.node {
		display: grid;
		grid-template-columns: 1.6em 1fr;
		gap: 12px;
		--tone: var(--st-info);
		--tone-t: var(--st-info-t);
	}
	.node.tone-changed {
		--tone: var(--st-changed);
		--tone-t: var(--st-changed-t);
	}
	.node.tone-critical {
		--tone: var(--st-critical);
		--tone-t: var(--st-critical-t);
	}
	.node.tone-nominal {
		--tone: var(--st-nominal);
		--tone-t: var(--st-nominal-t);
	}
	.marker {
		display: flex;
		flex-direction: column;
		align-items: center;
	}
	.glyph {
		font-size: 15px;
		line-height: 1.4;
		color: var(--tone-t);
	}
	.link {
		flex: 1;
		width: 2px;
		min-height: 18px;
		margin: 2px 0;
		background: var(--hairline-2);
	}
	.node.shown .link {
		background: color-mix(in srgb, var(--tone) 50%, var(--hairline-2));
	}
	.body {
		min-width: 0;
		padding-bottom: 14px;
	}
	.row1 {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 10px;
	}
	.seq {
		font-size: 12px;
		color: var(--text-1);
		font-weight: 600;
	}
	.slabel {
		font-size: 12px;
		color: var(--tone-t);
	}
	.time {
		font-size: 11px;
		color: var(--text-3);
		margin-left: auto;
	}
	.row2 {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 8px;
		margin-top: 4px;
	}
	.row2 .label {
		font-size: 11px;
		color: var(--text-3);
	}
	.hash {
		font-size: 12px;
		color: var(--text-2);
		background: var(--surface-inset);
		padding: 1px 6px;
		border-radius: var(--r-sm);
	}
	.mark {
		font-size: 12px;
	}
	.mark.ok {
		color: var(--st-nominal-t);
	}
	.mark.ng {
		color: var(--st-critical-t);
	}
	.mark.checking {
		color: var(--accent);
	}
	.mark.idle {
		color: var(--text-faint);
	}
	.node:target {
		background: var(--surface-2, rgba(127, 127, 127, 0.08));
		border-radius: var(--r-sm);
	}
	.node {
		scroll-margin-top: 80px;
	}

	.result {
		display: flex;
		gap: 12px;
		align-items: flex-start;
		margin: 14px 0 4px;
		padding: 12px 14px;
		border-radius: var(--r);
		border: 1px solid var(--hairline-2);
	}
	.result.ok {
		background: color-mix(in srgb, var(--st-nominal) 8%, transparent);
		border-color: color-mix(in srgb, var(--st-nominal) 40%, transparent);
	}
	.result.warn {
		background: color-mix(in srgb, var(--st-caution) 8%, transparent);
		border-color: color-mix(in srgb, var(--st-caution) 40%, transparent);
	}
	.rglyph {
		font-size: 18px;
		line-height: 1.4;
	}
	.result.ok .rglyph {
		color: var(--st-nominal-t);
	}
	.result.warn .rglyph {
		color: var(--st-caution-t);
	}
	.rtitle {
		margin: 0;
		font-size: 14px;
		color: var(--text-1);
	}
	.rsub {
		margin: 4px 0 0;
		font-size: 12px;
		line-height: 1.6;
		color: var(--text-2);
	}

	.panels {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 14px;
		margin-top: 18px;
	}
	@media (max-width: 36rem) {
		.panels {
			grid-template-columns: 1fr;
		}
	}
	.panel {
		background: var(--surface-2);
		border: 1px solid var(--hairline);
		border-radius: var(--r);
		padding: 12px 14px;
	}
	.panel h3.label {
		margin: 0 0 8px;
	}
	.cp-empty {
		margin: 0;
		font-size: 12px;
		line-height: 1.6;
		color: var(--text-3);
	}
	dl.cp {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 4px 12px;
		margin: 0;
		font-size: 12px;
	}
	dl.cp dt {
		color: var(--text-3);
	}
	dl.cp dd {
		margin: 0;
		color: var(--text-2);
	}
	dl.cp dd.hash {
		font-size: 11px;
	}
	ol.howto {
		margin: 0;
		padding: 0;
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	ol.howto li {
		display: grid;
		grid-template-columns: 1.6em 1fr;
		align-items: baseline;
		font-size: 12px;
		line-height: 1.6;
		color: var(--text-2);
	}
	ol.howto .n {
		color: var(--accent);
	}
	.howto-note {
		margin: 8px 0 0;
		font-size: 11px;
		line-height: 1.6;
		color: var(--text-3);
	}

	.tech {
		margin-top: 14px;
		font-size: 11px;
		color: var(--text-3);
	}
	.tech summary {
		cursor: pointer;
		color: var(--text-2);
	}
	.kv {
		display: grid;
		grid-template-columns: max-content 1fr;
		gap: 2px 12px;
		margin: 8px 0;
	}
	.kv dt {
		color: var(--text-3);
	}
	.kv dd {
		margin: 0;
		color: var(--text-2);
	}
	.hashes {
		width: 100%;
		border-collapse: collapse;
		font-size: 11px;
	}
	.hashes th,
	.hashes td {
		text-align: left;
		padding: 3px 8px 3px 0;
		color: var(--text-3);
	}
	.hashes tr.ng td {
		color: var(--st-critical-t);
	}
	.note {
		margin: 12px 0 0;
		font-size: 12px;
		line-height: 1.7;
		color: var(--text-3);
	}
</style>

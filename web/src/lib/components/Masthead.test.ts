import '@testing-library/jest-dom/vitest';
import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import Masthead from './Masthead.svelte';

// Pins the provenance-masthead contract (DESIGN_LANGUAGE §6 / ADR-000018): mono wordmark
// + text nav (no emoji), a stance line, and — critically — NO self-claimed "verified ✓"
// and NO "running" status (passive sentinel; integrity is not a per-record badge,
// ADR-000014 / 設計原則①). Coverage + checkpoint render only when their data exists.
describe('Masthead', () => {
	it('shows the wordmark linking home and the nav links', () => {
		render(Masthead);
		expect(screen.getByRole('link', { name: 'S4RCIV' })).toHaveAttribute('href', '/');
		expect(screen.getByRole('link', { name: '衆院' })).toHaveAttribute('href', '/votes');
		expect(screen.getByRole('link', { name: '参院' })).toHaveAttribute('href', '/sangiin');
	});

	it('states the passive, non-judging stance in plain language', () => {
		render(Masthead);
		expect(screen.getByText(/観測した事実を記録し、判断はしない/)).toBeInTheDocument();
	});

	it('uses no emoji and never self-claims "verified" or "running"', () => {
		const { container } = render(Masthead);
		const txt = container.textContent ?? '';
		expect(txt).not.toMatch(/[🗺📡✓]/u);
		expect(txt).not.toMatch(/検証済|稼働中|収集中/);
	});

	it('hides coverage and checkpoint when no data is given (no fake values)', () => {
		render(Masthead);
		expect(screen.queryByText(/監視/)).toBeNull();
		expect(screen.queryByText(/チェックポイント|seq#/)).toBeNull();
		expect(screen.queryByText(/▸検証/)).toBeNull();
	});

	it('shows coverage and a checkpoint with a verify link when provided', () => {
		render(Masthead, {
			props: {
				coverage: 1204,
				checkpoint: { seq: 8821, observedAt: '2026-06-06T09:00:00Z', verifyHref: '/verify' }
			}
		});
		expect(screen.getByText(/監視対象 1,204 件/)).toBeInTheDocument();
		expect(screen.getByText(/seq#8,821/)).toBeInTheDocument();
		expect(screen.getByRole('link', { name: '▸検証' })).toHaveAttribute('href', '/verify');
	});
});

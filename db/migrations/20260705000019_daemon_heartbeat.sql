-- Per-source daemon liveness (control plane, mutable operational state). The
-- collector daemon writes one row per source on every poll-loop tick; an
-- external healthcheck can then tell "process alive and looping" apart from
-- "process alive but silently wedged" (audit H-C3: no liveness signal existed).
CREATE TABLE control.daemon_heartbeat (
  source  text PRIMARY KEY REFERENCES control.source (source),
  beat_at timestamptz NOT NULL
);

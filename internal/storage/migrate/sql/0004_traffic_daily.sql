-- Daily traffic per tunnel.
--
-- The in-memory counters answer "what is happening now" and die with the
-- process; this answers "what did this tunnel carry last Tuesday", which is
-- the question a quota argument or a bandwidth bill turns on.
--
-- One row per tunnel per day rather than a row per transfer. A busy tunnel
-- serves tens of thousands of connections a day and a router's flash would
-- not survive recording them individually, nor would anyone read them.

CREATE TABLE traffic_daily (
    -- Local calendar day as YYYY-MM-DD. A string rather than unix seconds
    -- because the question is asked in days, and a day is a wall-clock idea:
    -- deriving it from a timestamp would need the timezone at every read.
    day         TEXT    NOT NULL,

    tunnel      TEXT    NOT NULL,

    -- Cumulative for that day. In is toward the local service, out is back
    -- toward the visitor, matching the direction names the counters use.
    bytes_in    INTEGER NOT NULL DEFAULT 0,
    bytes_out   INTEGER NOT NULL DEFAULT 0,

    -- Connections served, so an average transfer size is available without
    -- keeping every connection.
    connections INTEGER NOT NULL DEFAULT 0,

    updated_at  INTEGER NOT NULL,

    PRIMARY KEY (day, tunnel)
);

-- Reads are "the last N days, every tunnel" for the history view, and
-- "this tunnel over a range" for one tunnel's chart.
CREATE INDEX idx_traffic_daily_day ON traffic_daily (day DESC);

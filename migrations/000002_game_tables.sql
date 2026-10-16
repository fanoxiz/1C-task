CREATE TABLE game.loki (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name text NOT NULL CHECK (btrim(display_name) <> ''),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_seen_at timestamptz,
    connected boolean NOT NULL DEFAULT false,
    event_sequence bigint NOT NULL DEFAULT 0 CHECK (event_sequence >= 0),
    CONSTRAINT loki_connected_presence CHECK (NOT connected OR last_seen_at IS NOT NULL)
);

CREATE INDEX loki_presence ON game.loki (last_seen_at) WHERE connected;

CREATE TABLE game.invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    loki_id uuid NOT NULL REFERENCES game.loki (id),
    request_id uuid NOT NULL UNIQUE,
    mobius_side text NOT NULL CHECK (mobius_side IN ('X', 'O')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'declined', 'expired', 'cancelled')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    responded_at timestamptz,
    game_id uuid,
    statistics_snapshot jsonb NOT NULL CHECK (jsonb_typeof(statistics_snapshot) = 'object'),
    UNIQUE (id, loki_id),
    CONSTRAINT invitations_lifetime CHECK (expires_at > created_at),
    CONSTRAINT invitations_response CHECK (
        (status = 'pending' AND responded_at IS NULL) OR
        (status <> 'pending' AND responded_at IS NOT NULL AND responded_at >= created_at)
    )
);

CREATE UNIQUE INDEX invitations_one_pending ON game.invitations (loki_id) WHERE status = 'pending';
CREATE INDEX invitations_expiry ON game.invitations (expires_at) WHERE status = 'pending';

CREATE TABLE game.games (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    loki_id uuid NOT NULL REFERENCES game.loki (id),
    invitation_id uuid NOT NULL UNIQUE,
    mobius_side text NOT NULL CHECK (mobius_side IN ('X', 'O')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'completed')),
    result text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    UNIQUE (id, loki_id),
    UNIQUE (id, invitation_id),
    CONSTRAINT games_invitation_owner FOREIGN KEY (invitation_id, loki_id)
        REFERENCES game.invitations (id, loki_id),
    CONSTRAINT games_result CHECK (
        (status = 'active' AND result IS NULL AND finished_at IS NULL) OR
        (status = 'completed' AND result IS NOT NULL AND result IN ('loki_win', 'mobius_win', 'draw')
            AND finished_at IS NOT NULL AND finished_at >= created_at)
    )
);

CREATE UNIQUE INDEX games_one_active ON game.games (loki_id) WHERE status = 'active';
CREATE INDEX games_history ON game.games (loki_id, finished_at DESC, id DESC) WHERE status = 'completed';

ALTER TABLE game.invitations ADD CONSTRAINT invitations_game
    FOREIGN KEY (game_id, id) REFERENCES game.games (id, invitation_id) ON DELETE SET NULL (game_id);

CREATE TABLE game.moves (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id uuid NOT NULL REFERENCES game.games (id) ON DELETE CASCADE,
    number smallint NOT NULL CHECK (number BETWEEN 1 AND 9),
    actor text NOT NULL CHECK (actor IN ('mobius', 'loki')),
    row_index smallint NOT NULL CHECK (row_index BETWEEN 0 AND 2),
    column_index smallint NOT NULL CHECK (column_index BETWEEN 0 AND 2),
    request_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz,
    UNIQUE (id, game_id),
    CONSTRAINT moves_number UNIQUE (game_id, number),
    CONSTRAINT moves_cell UNIQUE (game_id, row_index, column_index),
    CONSTRAINT moves_request UNIQUE (game_id, request_id),
    CONSTRAINT moves_delivery CHECK (delivered_at IS NULL OR (actor = 'mobius' AND delivered_at >= created_at))
);

CREATE TABLE game.events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    loki_id uuid NOT NULL REFERENCES game.loki (id),
    sequence bigint NOT NULL CHECK (sequence > 0),
    type text NOT NULL CHECK (type IN ('invitation_created', 'invitation_closed', 'game_started', 'mobius_move', 'game_finished', 'game_cancelled')),
    game_id uuid,
    invitation_id uuid,
    move_id uuid,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    acked_at timestamptz,
    CONSTRAINT events_sequence UNIQUE (loki_id, sequence),
    CONSTRAINT events_game_owner FOREIGN KEY (game_id, loki_id)
        REFERENCES game.games (id, loki_id) ON DELETE CASCADE,
    CONSTRAINT events_invitation_owner FOREIGN KEY (invitation_id, loki_id)
        REFERENCES game.invitations (id, loki_id),
    CONSTRAINT events_move FOREIGN KEY (move_id, game_id)
        REFERENCES game.moves (id, game_id) ON DELETE CASCADE,
    CONSTRAINT events_move_reference CHECK (
        (type = 'mobius_move' AND move_id IS NOT NULL AND game_id IS NOT NULL) OR
        (type <> 'mobius_move' AND move_id IS NULL)
    ),
    CONSTRAINT events_cancellation CHECK (type <> 'game_cancelled' OR game_id IS NULL),
    CONSTRAINT events_ack_time CHECK (acked_at IS NULL OR acked_at >= created_at)
);

CREATE INDEX events_pending ON game.events (loki_id, sequence) WHERE acked_at IS NULL;

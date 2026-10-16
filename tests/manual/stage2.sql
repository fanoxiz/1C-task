\set ON_ERROR_STOP on
BEGIN;

-- Expected failures are caught inside subtransactions. Any unexpected result stops psql.
CREATE FUNCTION pg_temp.expect_constraint(command text, expected text) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    actual text;
BEGIN
    BEGIN
        EXECUTE command;
    EXCEPTION WHEN integrity_constraint_violation THEN
        GET STACKED DIAGNOSTICS actual = CONSTRAINT_NAME;
        IF actual <> expected THEN
            RAISE EXCEPTION 'Expected constraint %, got %', expected, actual;
        END IF;
        RAISE NOTICE 'OK: % rejected the invalid write', expected;
        RETURN;
    END;
    RAISE EXCEPTION 'Constraint % did not reject the invalid write', expected;
END;
$$;

DO $$
DECLARE
    loki_id uuid;
    first_invitation uuid;
    second_invitation uuid;
    game_id uuid;
    request_id uuid := gen_random_uuid();
BEGIN
    INSERT INTO game.loki (display_name, token_hash)
        VALUES ('Stage 2 manual check', uuid_send(gen_random_uuid()) || uuid_send(gen_random_uuid()))
        RETURNING id INTO loki_id;
    INSERT INTO game.invitations (loki_id, request_id, mobius_side, status, expires_at, responded_at, statistics_snapshot)
        VALUES (loki_id, gen_random_uuid(), 'X', 'accepted', clock_timestamp() + interval '30 seconds', clock_timestamp(), '{}')
        RETURNING id INTO first_invitation;
    INSERT INTO game.invitations (loki_id, request_id, mobius_side, status, expires_at, responded_at, statistics_snapshot)
        VALUES (loki_id, gen_random_uuid(), 'X', 'accepted', clock_timestamp() + interval '30 seconds', clock_timestamp(), '{}')
        RETURNING id INTO second_invitation;
    INSERT INTO game.games (loki_id, invitation_id, mobius_side)
        VALUES (loki_id, first_invitation, 'X') RETURNING id INTO game_id;
    INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id)
        VALUES (game_id, 1, 'mobius', 0, 0, request_id);
    RAISE NOTICE 'OK: a valid game and its first move were inserted';

    PERFORM pg_temp.expect_constraint(format(
        'INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id)
         VALUES (%L, 2, ''loki'', 0, 0, gen_random_uuid())', game_id), 'moves_cell');
    PERFORM pg_temp.expect_constraint(format(
        'INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id)
         VALUES (%L, 1, ''loki'', 0, 1, gen_random_uuid())', game_id), 'moves_number');
    PERFORM pg_temp.expect_constraint(format(
        'INSERT INTO game.moves (game_id, number, actor, row_index, column_index, request_id)
         VALUES (%L, 2, ''loki'', 0, 1, %L)', game_id, request_id), 'moves_request');
    PERFORM pg_temp.expect_constraint(format(
        'INSERT INTO game.games (loki_id, invitation_id, mobius_side)
         VALUES (%L, %L, ''X'')', loki_id, second_invitation), 'games_one_active');
    PERFORM pg_temp.expect_constraint(format(
        'UPDATE game.moves SET row_index = 3 WHERE game_id = %L', game_id), 'moves_row_index_check');
    PERFORM pg_temp.expect_constraint(format(
        'UPDATE game.games SET status = ''completed'', finished_at = clock_timestamp() WHERE id = %L', game_id), 'games_result');
END;
$$;

ROLLBACK;
\echo 'Stage 2 storage checks passed. All test data was rolled back.'

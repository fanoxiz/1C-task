CREATE INDEX invitations_history ON game.invitations (loki_id, created_at DESC, id DESC);

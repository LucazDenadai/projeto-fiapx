-- +goose Up
-- Vídeos (ADR-0014) e a outbox (ADR-0003).
CREATE TABLE videos (
    id            uuid        PRIMARY KEY,
    usuario_id    uuid        NOT NULL REFERENCES usuarios (id),
    nome_original text        NOT NULL,
    status        text        NOT NULL
        CHECK (status IN ('recebido', 'processando', 'concluido', 'falhou')),
    motivo        text
        CHECK (motivo IN ('video_invalido', 'video_muito_longo', 'sem_frames', 'tempo_excedido', 'erro_interno')),
    criado_em     timestamptz NOT NULL DEFAULT now(),
    atualizado_em timestamptz NOT NULL DEFAULT now(),
    zip_expira_em timestamptz,
    -- O Motivo da falha existe só, e sempre, no status falhou.
    CHECK ((status = 'falhou') = (motivo IS NOT NULL))
);

-- Listagem do Usuário, do mais recente para o mais antigo, por cursor.
CREATE INDEX videos_usuario_criado_em ON videos (usuario_id, criado_em DESC, id DESC);

CREATE TABLE outbox (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    exchange     text        NOT NULL,
    routing_key  text        NOT NULL,
    payload      jsonb       NOT NULL,
    criado_em    timestamptz NOT NULL DEFAULT now(),
    publicado_em timestamptz
);

-- Polling do relay: só as linhas pendentes.
CREATE INDEX outbox_pendentes ON outbox (id) WHERE publicado_em IS NULL;

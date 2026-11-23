-- +goose Up
-- Usuários (ADR-0007): e-mail único já em minúsculas e senha só como hash
-- argon2id em formato PHC.
CREATE TABLE usuarios (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email      text        NOT NULL UNIQUE CHECK (email = lower(email)),
    senha_hash text        NOT NULL,
    criado_em  timestamptz NOT NULL DEFAULT now()
);

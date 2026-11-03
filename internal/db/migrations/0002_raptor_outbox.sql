CREATE TABLE raptor_outbox (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    kind        VARCHAR(64)     NOT NULL,
    payload     MEDIUMTEXT      NOT NULL,
    state       VARCHAR(16)     NOT NULL DEFAULT 'pending',
    attempts    INT UNSIGNED    NOT NULL DEFAULT 0,
    run_at      TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_until TIMESTAMP       NULL,
    failure     TEXT            NULL,
    created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY raptor_outbox_due (state, run_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

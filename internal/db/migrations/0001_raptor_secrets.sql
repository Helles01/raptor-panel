CREATE TABLE raptor_secrets (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    table_name  VARCHAR(64)     NOT NULL,
    row_id      BIGINT UNSIGNED NOT NULL,
    column_name VARCHAR(64)     NOT NULL,
    ciphertext  MEDIUMTEXT      NOT NULL,
    source_hash CHAR(64)        NULL,
    created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY raptor_secrets_location (table_name, row_id, column_name)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;

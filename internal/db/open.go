// Package db opens the MariaDB/MySQL connection, owns Raptor's additive migrations and checks that the database has
// the schema of the reference panel.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Helles01/raptor-panel/internal/config"
)

// Open connects to the database and checks the connection. cfg must already be validated.
func Open(ctx context.Context, cfg config.DB) (*sql.DB, error) {
	connector, err := mysql.NewConnector(driverConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("configure database connection: %w", err)
	}

	sqlDB := sql.OpenDB(connector)
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("connect to database %s: %w", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), err)
	}
	return sqlDB, nil
}

// driverConfig maps the Raptor settings to the driver. Multiple statements per call, local file loading and cleartext
// passwords stay at the driver's safe defaults (off).
func driverConfig(cfg config.DB) *mysql.Config {
	c := mysql.NewConfig()
	c.User = cfg.User
	c.Passwd = cfg.Password
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	c.DBName = cfg.Name
	c.Collation = "utf8mb4_unicode_ci"
	c.ParseTime = true
	c.Loc = time.UTC
	c.TLSConfig = cfg.TLS
	c.Timeout = 10 * time.Second
	c.ReadTimeout = 30 * time.Second
	c.WriteTimeout = 30 * time.Second
	return c
}

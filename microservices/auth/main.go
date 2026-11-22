// Command auth is the gRPC service that owns users and credentials.
package main

import (
	"context"
	_ "embed"
	"errors"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"photoshare/internal/dbutil"
	"photoshare/internal/jwtutil"
	"photoshare/microservices/pb/authpb"
)

//go:embed schema.sql
var schema string

const tokenTTL = 24 * time.Hour

type server struct {
	authpb.UnimplementedAuthServer
	db     *pgxpool.Pool
	secret []byte
}

func main() {
	ctx := context.Background()
	db, err := dbutil.Connect(ctx, env("DATABASE_URL", "postgres://auth:auth@localhost:5432/auth"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(ctx, schema); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	lis, err := net.Listen("tcp", env("GRPC_ADDR", ":9001"))
	if err != nil {
		log.Fatal(err)
	}
	s := grpc.NewServer()
	authpb.RegisterAuthServer(s, &server{db: db, secret: []byte(env("JWT_SECRET", "dev-secret-change-me"))})
	log.Printf("auth listening on %s", lis.Addr())
	log.Fatal(s.Serve(lis))
}

func (s *server) Register(ctx context.Context, c *authpb.Credentials) (*authpb.User, error) {
	if !validCredentials(c) {
		return nil, status.Error(codes.InvalidArgument, "username and password are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "password cannot be hashed")
	}
	var id int64
	err = s.db.QueryRow(ctx,
		`INSERT INTO users (username, password_hash) VALUES ($1, $2) RETURNING id`,
		c.Username, string(hash)).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, status.Error(codes.AlreadyExists, "username already taken")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	return &authpb.User{Id: id, Username: c.Username}, nil
}

func (s *server) Login(ctx context.Context, c *authpb.Credentials) (*authpb.Token, error) {
	if !validCredentials(c) {
		return nil, status.Error(codes.InvalidArgument, "username and password are required")
	}
	var id int64
	var hash string
	err := s.db.QueryRow(ctx,
		`SELECT id, password_hash FROM users WHERE username = $1`, c.Username).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil) {
		return nil, status.Error(codes.Unauthenticated, "wrong username or password")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	token, err := jwtutil.Sign(s.secret, id, tokenTTL)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot sign token")
	}
	return &authpb.Token{Token: token}, nil
}

func (s *server) UserExists(ctx context.Context, ref *authpb.UserRef) (*authpb.ExistsReply, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, ref.Id).Scan(&ok)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	return &authpb.ExistsReply{Exists: ok}, nil
}

func (s *server) GetUsers(ctx context.Context, req *authpb.GetUsersRequest) (*authpb.GetUsersReply, error) {
	rows, err := s.db.Query(ctx, `SELECT id, username FROM users WHERE id = ANY($1)`, req.Ids)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	defer rows.Close()
	names := make(map[int64]string, len(req.Ids))
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, status.Error(codes.Unavailable, "database error")
		}
		names[id] = name
	}
	if rows.Err() != nil {
		return nil, status.Error(codes.Unavailable, "database error")
	}
	return &authpb.GetUsersReply{Names: names}, nil
}

// validCredentials rejects empty values and NUL bytes, which Postgres cannot store.
func validCredentials(c *authpb.Credentials) bool {
	return c.Username != "" && c.Password != "" &&
		!strings.ContainsRune(c.Username, 0) && !strings.ContainsRune(c.Password, 0)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

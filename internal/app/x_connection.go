package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"reposter/internal/x"
)

var ErrXAccountMismatch = errors.New("authorized X account does not match destination")
var ErrXNotConnected = errors.New("X destination is not connected")

func (s *Service) XConnectionStart(ctx context.Context, destinationID int64) (string, string, error) {
	if !s.XTokenCipher.Enabled() {
		return "", "", errors.New("X token encryption key is not configured")
	}
	if _, err := s.Store.XDestination(ctx, destinationID); err != nil {
		return "", "", err
	}
	token, secret, authURL, err := s.X.BeginPIN(ctx)
	if err != nil {
		return "", "", err
	}
	encrypted, err := s.XTokenCipher.Seal(secret)
	if err != nil {
		return "", "", err
	}
	if err = s.Store.SaveXOAuthRequest(ctx, destinationID, token, encrypted); err != nil {
		return "", "", err
	}
	return token, authURL, nil
}
func (s *Service) XConnectionComplete(ctx context.Context, destinationID int64, requestToken, pin string) (string, error) {
	if !s.XTokenCipher.Enabled() {
		return "", errors.New("X token encryption key is not configured")
	}
	destination, err := s.Store.XDestination(ctx, destinationID)
	if err != nil {
		return "", err
	}
	encryptedSecret, err := s.Store.XOAuthRequest(ctx, destinationID, requestToken)
	if err != nil {
		return "", err
	}
	secret, err := s.XTokenCipher.Open(encryptedSecret)
	if err != nil {
		return "", err
	}
	token, userSecret, err := s.X.CompletePIN(ctx, requestToken, secret, pin)
	if err != nil {
		return "", err
	}
	client := s.X
	client.AccessToken = token
	client.AccessSecret = userSecret
	username, err := client.AuthenticatedUsername(ctx)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(username, strings.TrimPrefix(destination.ChatID, "@")) {
		return "", fmt.Errorf("%w: got @%s, expected %s", ErrXAccountMismatch, username, destination.ChatID)
	}
	encryptedToken, err := s.XTokenCipher.Seal(token)
	if err != nil {
		return "", err
	}
	encryptedUserSecret, err := s.XTokenCipher.Seal(userSecret)
	if err != nil {
		return "", err
	}
	if err = s.Store.SaveXConnection(ctx, destinationID, username, encryptedToken, encryptedUserSecret, requestToken); err != nil {
		return "", err
	}
	return username, nil
}
func (s *Service) xClientForDestination(ctx context.Context, destinationID int64) (x.Client, error) {
	username, tokenCipher, secretCipher, err := s.Store.XConnection(ctx, destinationID)
	if errors.Is(err, pgx.ErrNoRows) {
		if s.X.CanPublish() {
			return s.X, nil
		}
		return x.Client{}, ErrXNotConnected
	}
	if err != nil {
		return x.Client{}, err
	}
	if username == "" {
		return x.Client{}, errors.New("X connection has no username")
	}
	token, err := s.XTokenCipher.Open(tokenCipher)
	if err != nil {
		return x.Client{}, err
	}
	secret, err := s.XTokenCipher.Open(secretCipher)
	if err != nil {
		return x.Client{}, err
	}
	client := s.X
	client.AccessToken = token
	client.AccessSecret = secret
	return client, nil
}
func (s *Service) XTestPost(ctx context.Context, destinationID int64, body string) (string, error) {
	destination, err := s.Store.XDestination(ctx, destinationID)
	if err != nil {
		return "", err
	}
	client, err := s.xClientForDestination(ctx, destinationID)
	if err != nil {
		return "", err
	}
	if err = client.VerifyPublisher(ctx, strings.TrimPrefix(destination.ChatID, "@")); err != nil {
		return "", err
	}
	postID, err := client.Publish(ctx, body, nil)
	if x.IsOAuthPermissionError(err) {
		if disableErr := s.Store.DisableXPublishing(ctx, destinationID); disableErr != nil {
			return "", errors.Join(err, disableErr)
		}
	}
	return postID, err
}

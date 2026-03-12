package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func runUserDataLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	for {
		if ctx.Err() != nil {
			return
		}

		state.setAccountError("connecting user data stream...")
		notify()

		listenKey, err := createListenKey(ctx, client, cfg)
		if err != nil {
			state.setAccountError(fmt.Sprintf("user data stream start failed: %v", err))
			notify()
		} else {
			state.setAccountError("")
			notify()
			err = consumeUserDataStream(ctx, client, cfg, state, notify, listenKey)
			if err == nil || ctx.Err() != nil {
				return
			}
			state.setAccountError(fmt.Sprintf("user data stream disconnected: %v | retry in %s", err, cfg.RetryDelay))
			notify()
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeUserDataStream(ctx context.Context, client *http.Client, cfg config, state *appState, notify func(), listenKey string) error {
	endpoint := strings.TrimRight(cfg.WSBase, "/") + "/ws/" + listenKey
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial user data websocket: %w", err)
	}
	defer conn.Close()
	defer closeListenKey(context.Background(), client, cfg, listenKey)

	keepaliveTicker := time.NewTicker(userDataKeepaliveInterval)
	defer keepaliveTicker.Stop()
	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	})

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			var payload userDataEvent
			if err := conn.ReadJSON(&payload); err != nil {
				readErrCh <- err
				return
			}

			switch payload.EventType {
			case "ACCOUNT_UPDATE":
				updates, err := parseAccountUpdateEvent(payload)
				if err != nil {
					readErrCh <- fmt.Errorf("decode account update payload: %w", err)
					return
				}
				state.applyPositionUpdates(updates)
				state.clearAccountError()
				notify()
			case "listenKeyExpired":
				readErrCh <- errors.New("listen key expired")
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return nil
		case err := <-readErrCh:
			if err == nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, netErrClosed) {
				return err
			}
			return err
		case <-keepaliveTicker.C:
			if err := keepaliveListenKey(ctx, client, cfg, listenKey); err != nil {
				return fmt.Errorf("keepalive listen key: %w", err)
			}
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(time.Second)); err != nil {
				return fmt.Errorf("ping user data websocket: %w", err)
			}
		}
	}
}

func runSpotUserDataLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	for {
		if ctx.Err() != nil {
			return
		}

		state.setSpotAccountError("connecting spot user data stream...")
		notify()

		err := consumeSpotUserDataStream(ctx, client, cfg, state, notify)
		if err == nil || ctx.Err() != nil {
			return
		}

		state.setSpotAccountError(fmt.Sprintf("spot user data stream disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeSpotUserDataStream(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) error {
	endpoint := defaultSpotWSAPIBaseURL
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial spot user data websocket api: %w", err)
	}
	defer conn.Close()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	})

	timestamp := time.Now().UnixMilli()
	recvWindow := int64(cfg.Timeout / time.Millisecond)
	if recvWindow <= 0 {
		recvWindow = 5000
	}
	signature := signSpotUserDataStreamRequest(cfg.APIKey, cfg.APISecret, timestamp, recvWindow)
	request := map[string]any{
		"id":     fmt.Sprintf("spot-user-stream-%d", timestamp),
		"method": "userDataStream.subscribe.signature",
		"params": map[string]any{
			"apiKey":     cfg.APIKey,
			"timestamp":  timestamp,
			"recvWindow": recvWindow,
			"signature":  signature,
		},
	}
	if err := conn.WriteJSON(request); err != nil {
		return fmt.Errorf("subscribe spot user data stream: %w", err)
	}

	allowed := allowedSpotAssets(cfg.SpotSymbols)
	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				readErrCh <- err
				return
			}

			var eventEnvelope struct {
				SubscriptionID int             `json:"subscriptionId"`
				Event          json.RawMessage `json:"event"`
			}
			if err := json.Unmarshal(message, &eventEnvelope); err == nil && len(eventEnvelope.Event) > 0 {
				var payload spotUserDataEvent
				if err := json.Unmarshal(eventEnvelope.Event, &payload); err != nil {
					readErrCh <- fmt.Errorf("decode spot user event type: %w", err)
					return
				}

				switch payload.EventType {
				case "outboundAccountPosition":
					updates, err := parseSpotAccountUpdateEvent(eventEnvelope.Event, allowed)
					if err != nil {
						readErrCh <- fmt.Errorf("decode spot account update payload: %w", err)
						return
					}
					state.applySpotBalanceUpdates(updates)
					state.clearSpotAccountError()
					notify()
				case "balanceUpdate", "externalLockUpdate":
					balances, err := fetchSpotBalances(ctx, client, cfg)
					if err != nil {
						state.setSpotAccountError(fmt.Sprintf("spot account refresh failed: %v", err))
					} else {
						state.setSpotBalances(balances)
						state.clearSpotAccountError()
					}
					notify()
				case "eventStreamTerminated":
					readErrCh <- errors.New("spot user data stream terminated")
					return
				}
				continue
			}

			var response struct {
				Status int `json:"status"`
				Error  *struct {
					Code int    `json:"code"`
					Msg  string `json:"msg"`
				} `json:"error"`
			}
			if err := json.Unmarshal(message, &response); err != nil {
				readErrCh <- fmt.Errorf("decode spot websocket api payload: %w", err)
				return
			}
			if response.Status == 0 {
				continue
			}
			if response.Status != http.StatusOK {
				if response.Error != nil {
					readErrCh <- fmt.Errorf("spot subscribe failed: code %d: %s", response.Error.Code, response.Error.Msg)
				} else {
					readErrCh <- fmt.Errorf("spot subscribe failed: status %d", response.Status)
				}
				return
			}

			state.clearSpotAccountError()
			notify()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return nil
		case err := <-readErrCh:
			if err == nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, netErrClosed) {
				return err
			}
			return err
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(time.Second)); err != nil {
				return fmt.Errorf("ping spot user data websocket api: %w", err)
			}
		}
	}
}

func signSpotUserDataStreamRequest(apiKey, secret string, timestamp, recvWindow int64) string {
	payload := fmt.Sprintf("apiKey=%s&recvWindow=%d&timestamp=%d", apiKey, recvWindow, timestamp)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func parseSpotAccountUpdateEvent(event json.RawMessage, allowed map[string]struct{}) ([]spotBalanceUpdate, error) {
	var payload spotOutboundAccountPositionEvent
	if err := json.Unmarshal(event, &payload); err != nil {
		return nil, err
	}

	updates := make([]spotBalanceUpdate, 0, len(payload.Balances))
	for _, item := range payload.Balances {
		update, ok, err := parseSpotBalanceUpdatePayload(item, int64(payload.UpdateTime), allowed)
		if err != nil {
			return nil, err
		}
		if ok {
			updates = append(updates, update)
		}
	}
	return updates, nil
}

func parseSpotBalanceUpdatePayload(item spotBalanceUpdatePayload, updateTime int64, allowed map[string]struct{}) (spotBalanceUpdate, bool, error) {
	asset := strings.ToUpper(strings.TrimSpace(item.Asset))
	if _, ok := allowed[asset]; !ok {
		return spotBalanceUpdate{}, false, nil
	}

	free, err := strconv.ParseFloat(string(item.Free), 64)
	if err != nil {
		return spotBalanceUpdate{}, false, fmt.Errorf("parse spot free update for %s: %w", asset, err)
	}
	locked, err := strconv.ParseFloat(string(item.Locked), 64)
	if err != nil {
		return spotBalanceUpdate{}, false, fmt.Errorf("parse spot locked update for %s: %w", asset, err)
	}

	return spotBalanceUpdate{
		Asset:      asset,
		Free:       free,
		Locked:     locked,
		UpdateTime: updateTime,
		Remove:     free+locked <= 0,
	}, true, nil
}

func createListenKey(ctx context.Context, client *http.Client, cfg config) (string, error) {
	resp, err := doListenKeyRequest(ctx, client, cfg, http.MethodPost, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read listen key response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listen key status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload userDataListenKeyResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode listen key response: %w", err)
	}
	if strings.TrimSpace(payload.ListenKey) == "" {
		return "", errors.New("missing listen key in response")
	}
	return payload.ListenKey, nil
}

func keepaliveListenKey(ctx context.Context, client *http.Client, cfg config, listenKey string) error {
	resp, err := doListenKeyRequest(ctx, client, cfg, http.MethodPut, listenKey)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read keepalive response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keepalive status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func closeListenKey(ctx context.Context, client *http.Client, cfg config, listenKey string) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	resp, err := doListenKeyRequest(ctx, client, cfg, http.MethodDelete, listenKey)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func doListenKeyRequest(ctx context.Context, client *http.Client, cfg config, method, listenKey string) (*http.Response, error) {
	parsed, err := url.Parse(cfg.RESTBase)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = listenKeyPath
	if listenKey != "" {
		query := url.Values{}
		query.Set("listenKey", listenKey)
		parsed.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build listen key request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send listen key request: %w", err)
	}
	return resp, nil
}

func parseAccountUpdateEvent(event userDataEvent) ([]positionUpdate, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}

	var payload userDataAccountUpdateEvent
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}

	updates := make([]positionUpdate, 0, len(payload.Data.Positions))
	for _, item := range payload.Data.Positions {
		update, err := parsePositionUpdatePayload(item, int64(payload.EventTime))
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, nil
}

func parsePositionUpdatePayload(item userDataPositionPayload, updateTime int64) (positionUpdate, error) {
	size, err := strconv.ParseFloat(string(item.PositionAmt), 64)
	if err != nil {
		return positionUpdate{}, fmt.Errorf("parse position update size for %s: %w", item.Symbol, err)
	}
	entryPrice, err := strconv.ParseFloat(string(item.EntryPrice), 64)
	if err != nil {
		return positionUpdate{}, fmt.Errorf("parse position update entry for %s: %w", item.Symbol, err)
	}
	unrealizedPnL, err := strconv.ParseFloat(string(item.UnrealizedProfit), 64)
	if err != nil {
		return positionUpdate{}, fmt.Errorf("parse position update pnl for %s: %w", item.Symbol, err)
	}

	side := strings.ToUpper(strings.TrimSpace(item.PositionSide))
	if side == "" || side == "BOTH" {
		if size > 0 {
			side = "LONG"
		} else if size < 0 {
			side = "SHORT"
		}
	}

	return positionUpdate{
		Symbol:        strings.ToUpper(strings.TrimSpace(item.Symbol)),
		Side:          side,
		Size:          math.Abs(size),
		EntryPrice:    entryPrice,
		UnrealizedPnL: unrealizedPnL,
		MarginType:    strings.ToUpper(strings.TrimSpace(item.MarginType)),
		UpdateTime:    updateTime,
		Remove:        math.Abs(size) < 1e-12,
	}, nil
}

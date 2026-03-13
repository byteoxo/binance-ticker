package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

type wsEnvelope struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

type wsKlineEnvelope struct {
	Symbol string         `json:"s"`
	Kline  wsKlinePayload `json:"k"`
}

type wsKlinePayload struct {
	StartTime jsonFlexibleInt64  `json:"t"`
	CloseTime jsonFlexibleInt64  `json:"T"`
	Open      jsonFlexibleString `json:"o"`
	High      jsonFlexibleString `json:"h"`
	Low       jsonFlexibleString `json:"l"`
	Close     jsonFlexibleString `json:"c"`
	Volume    jsonFlexibleString `json:"v"`
	IsClosed  bool               `json:"x"`
}

type userDataListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

type userDataAccountUpdateEvent struct {
	EventType string                `json:"e"`
	EventTime jsonFlexibleInt64     `json:"E"`
	Data      userDataAccountUpdate `json:"a"`
}

type userDataAccountUpdate struct {
	Reason    string                    `json:"m"`
	Positions []userDataPositionPayload `json:"P"`
}

type userDataPositionPayload struct {
	Symbol           string             `json:"s"`
	PositionAmt      jsonFlexibleString `json:"pa"`
	EntryPrice       jsonFlexibleString `json:"ep"`
	UnrealizedProfit jsonFlexibleString `json:"up"`
	MarginType       string             `json:"mt"`
	PositionSide     string             `json:"ps"`
}

type spotUserDataEvent struct {
	EventType string `json:"e"`
}

type spotOutboundAccountPositionEvent struct {
	EventType  string                     `json:"e"`
	EventTime  jsonFlexibleInt64          `json:"E"`
	UpdateTime jsonFlexibleInt64          `json:"u"`
	Balances   []spotBalanceUpdatePayload `json:"B"`
}

type spotBalanceUpdatePayload struct {
	Asset  string             `json:"a"`
	Free   jsonFlexibleString `json:"f"`
	Locked jsonFlexibleString `json:"l"`
}

type spotAssetDeltaEvent struct {
	EventType string             `json:"e"`
	EventTime jsonFlexibleInt64  `json:"E"`
	Asset     string             `json:"a"`
	Delta     jsonFlexibleString `json:"d"`
	ClearTime jsonFlexibleInt64  `json:"T"`
}

type spotBalanceUpdate struct {
	Asset      string
	Free       float64
	Locked     float64
	UpdateTime int64
	Remove     bool
}

type jsonFlexibleInt64 int64

type jsonFlexibleString string

func (v *jsonFlexibleInt64) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*v = 0
		return nil
	}

	var num int64
	if err := json.Unmarshal(data, &num); err == nil {
		*v = jsonFlexibleInt64(num)
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}

	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}

	*v = jsonFlexibleInt64(parsed)
	return nil
}

func (v *jsonFlexibleString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*v = ""
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*v = jsonFlexibleString(text)
		return nil
	}

	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return err
	}

	*v = jsonFlexibleString(number.String())
	return nil
}

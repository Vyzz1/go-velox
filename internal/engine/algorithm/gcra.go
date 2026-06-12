// Package algorithm implements the Generic Cell Rate Algorithm (GCRA),
// also known as the virtual scheduling algorithm.
//
// GCRA is O(1) in time and space per key. It tracks a single "theoretical
// arrival time" (TAT) per rate-limited key and makes allow/deny decisions
// in one atomic Redis operation with no approximation errors.
package algorithm

import "time"

type AlgorithmType uint8

const (
	GCRA          AlgorithmType = iota // token bucket / leaky bucket hybrid; supports burst
	SlidingWindow                      // weighted two-window counter; no burst, smooth window
)

// Params defines the rate-limit policy for one evaluation.
type Params struct {
	Algorithm AlgorithmType
	Limit     uint64        // maximum requests allowed per Period
	Period    time.Duration // the window duration
	Burst     uint64        // extra burst capacity — GCRA only (0 → defaults to Limit)
	Cost      uint64        // cost of this particular request (0 → treated as 1)
}

// Result is the outcome of one rate-limit check.
type Result struct {
	Allowed      bool
	Remaining    uint64
	ResetAtMs    int64 // unix ms when the limit resets for this key
	RetryAfterMs int64 // ms until the next request would be allowed (0 when Allowed)
}

// Script is the Redis Lua implementation of GCRA.
//
// Inputs:
//
//	KEYS[1]  — rate-limit key
//	ARGV[1]  — limit  (uint, requests per period)
//	ARGV[2]  — period in milliseconds
//	ARGV[3]  — cost   (uint, this request's weight; ≥1)
//	ARGV[4]  — now    (int64, current unix time in ms)
//	ARGV[5]  — burst  (uint, extra capacity; ≥1)
//
// Returns: {allowed(0|1), remaining, reset_at_ms, retry_after_ms}
const Script = `
local key        = KEYS[1]
local limit      = tonumber(ARGV[1])
local period_ms  = tonumber(ARGV[2])
local cost       = tonumber(ARGV[3])
local now_ms     = tonumber(ARGV[4])
local burst      = tonumber(ARGV[5])

if burst < 1 then burst = 1 end

local emission   = period_ms / limit   -- ms per token
local burst_off  = emission * burst    -- total burst window in ms
local increment  = emission * cost     -- ms consumed by this request

local tat = tonumber(redis.call("GET", key))
if not tat then tat = now_ms end

local new_tat   = math.max(tat, now_ms) + increment
local allow_at  = new_tat - burst_off
local diff      = now_ms - allow_at

if diff >= 0 then
    local ttl_ms = math.ceil(new_tat - now_ms + burst_off)
    if ttl_ms < 1 then ttl_ms = 1 end
    redis.call("SET", key, tostring(new_tat), "PX", ttl_ms)
    local remaining = math.floor(diff / emission)
    return {1, remaining, math.ceil(new_tat), 0}
else
    return {0, 0, math.ceil(new_tat - burst_off), math.ceil(-diff)}
end
`

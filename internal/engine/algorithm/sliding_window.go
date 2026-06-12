package algorithm

// SlidingWindowScript is a Redis Lua implementation of the sliding window counter.
//
// It approximates a true sliding window by weighting two adjacent fixed windows:
//
//	estimate = prev_count * (1 - elapsed/window) + curr_count
//
// This gives a smooth, accurate count without storing per-request timestamps.
// No burst parameter — requests are evenly distributed across the window.
//
// Redis Cluster note: KEYS[1] must include a hash tag, e.g. "{rl:tenant:rule}".
// The script derives two sub-keys internally; the shared hash tag guarantees
// both land on the same cluster slot.
//
// Inputs:
//
//	KEYS[1]  — base key with hash tag, e.g. "{rl:tenant:rule:resource:action:subject}"
//	ARGV[1]  — limit  (max requests per window)
//	ARGV[2]  — window in milliseconds
//	ARGV[3]  — cost
//	ARGV[4]  — now (unix ms)
//
// Returns: {allowed(0|1), remaining, reset_at_ms, retry_after_ms}
const SlidingWindowScript = `
local base      = KEYS[1]
local limit     = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local cost      = tonumber(ARGV[3])
local now_ms    = tonumber(ARGV[4])

local curr_win = math.floor(now_ms / window_ms) * window_ms
local prev_win = curr_win - window_ms
local elapsed  = now_ms - curr_win

local key_curr = base .. ":" .. tostring(curr_win)
local key_prev = base .. ":" .. tostring(prev_win)

local prev_count = tonumber(redis.call("GET", key_prev)) or 0
local curr_count = tonumber(redis.call("GET", key_curr)) or 0

local weight   = 1 - elapsed / window_ms
local estimate = prev_count * weight + curr_count

if estimate + cost > limit then
    local ms_per_req  = window_ms / limit
    local retry_after = math.ceil((estimate + cost - limit) * ms_per_req)
    if retry_after < 1 then retry_after = 1 end
    return {0, 0, curr_win + window_ms, retry_after}
end

redis.call("INCRBY", key_curr, cost)
redis.call("PEXPIRE", key_curr, window_ms * 2)

local remaining = math.floor(limit - (prev_count * weight + curr_count + cost))
if remaining < 0 then remaining = 0 end

return {1, remaining, curr_win + window_ms, 0}
`

package main

import "github.com/anacrolix/log"

// defaultLogLevel keeps the torrent library quiet. It logs per-peer chatter at
// debug, which on a busy swarm is thousands of lines a minute.
func defaultLogLevel() log.Level { return log.Warning }

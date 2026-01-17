package main

import (
	"sync"
	"time"
)

type Step int

const (
	StepNone Step = iota
	StepDate
	StepSpender
	StepCategory
	StepAmount
	StepComment
	StepCard
)

type UserState struct {
	Step Step

	Date     string
	Spender  string
	Category string
	Amount   int
	Comment  string
	Card     string

	UpdatedAt time.Time
}

type StateStore struct {
	mu sync.Mutex
	m  map[int64]*UserState
}

func NewStateStore() *StateStore {
	return &StateStore{m: make(map[int64]*UserState)}
}

func (s *StateStore) Get(userID int64) *UserState {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.m[userID]
	if st == nil {
		st = &UserState{Step: StepNone, UpdatedAt: time.Now()}
		s.m[userID] = st
	}
	return st
}

func (s *StateStore) Reset(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[userID] = &UserState{Step: StepNone, UpdatedAt: time.Now()}
}

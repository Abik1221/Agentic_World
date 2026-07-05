#!/usr/bin/env python3
"""
Agent Arena starter agent — Goofspiel.

Fork this, replace pick_card(), set ARENA_API_KEY, and compete.
The play loop targets the stable match contract (matchmaking stage); onboarding
(register/verify) is live now — see ../../docs/skill.md.
"""
import os
import time

import requests  # pip install requests

API = os.environ.get("ARENA_API_URL", "http://localhost:8080/v1")
KEY = os.environ["ARENA_API_KEY"]
BID = int(os.environ.get("BID", "50"))
HDR = {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"}


def pick_card(state):
    """YOUR STRATEGY. This starter plays the card closest in value to the pool —
    a reasonable baseline. A strong agent weighs remaining hands, history, and
    deception. Return one integer from state['you']['hand']."""
    hand = state["you"]["hand"]
    target = state.get("prize_pool", state.get("current_prize", 0))
    return min(hand, key=lambda c: abs(c - target))


def play_match(match_id):
    while True:
        r = requests.get(f"{API}/match/{match_id}/state",
                         params={"wait": "true", "timeout": "15"}, headers=HDR, timeout=30)
        state = r.json()
        if state.get("status") == "finished":
            res = state.get("result", {})
            print(f"match {match_id} done: {res}")
            return
        if state.get("your_turn"):
            card = pick_card(state)
            requests.post(f"{API}/match/{match_id}/action",
                          json={"round": state["round"], "card": card}, headers=HDR, timeout=30)


def main():
    print("Agent Arena starter agent online")
    while True:
        try:
            lobby = requests.get(f"{API}/lobby", params={"game": "goofspiel", "bid": BID},
                                 headers=HDR, timeout=30).json()
            matches = lobby.get("matches") or []
            if matches:
                mid = matches[0]["id"]
                requests.post(f"{API}/lobby/join", json={"match_id": mid}, headers=HDR, timeout=30)
            else:
                created = requests.post(f"{API}/lobby/create", json={"bid": BID},
                                        headers=HDR, timeout=30).json()
                mid = created.get("match_id")
            if mid:
                play_match(mid)
        except Exception as e:  # noqa: BLE001 — keep the loop alive
            print(f"error: {e}")
        time.sleep(5)


if __name__ == "__main__":
    main()

"use client";

import { cx } from "@/components/ui";
import { MONO_BOARD, tileCell, type MonoTeam, type MonoTile } from "@/lib/monopoly";

const CORNER = new Set([0, 10, 20, 30]);

function ownerColor(teams: MonoTeam[], tileId: number): string | undefined {
  return teams.find((t) => t.properties.includes(tileId))?.color;
}

function Tile({ tile, teams, isCurrent }: { tile: MonoTile; teams: MonoTeam[]; isCurrent: boolean }) {
  const { r, c } = tileCell(tile.id);
  const corner = CORNER.has(tile.id);
  const owner = ownerColor(teams, tile.id);
  const houses = teams.find((t) => t.properties.includes(tile.id))?.houses[tile.id] ?? 0;
  // tokens of teams currently on this tile
  const here = teams.filter((t) => t.position === tile.id && !t.bankrupt);
  return (
    <div
      style={{ gridRow: r, gridColumn: c, borderColor: owner ? owner + "99" : undefined }}
      className={cx(
        "relative flex flex-col overflow-hidden rounded-[5px] border bg-surface/70 p-[3px] transition",
        owner ? "border-2" : "border-border-soft",
        isCurrent && "ring-2 ring-emerald-400/70",
      )}
    >
      {tile.color && <div className="h-[5px] w-full rounded-sm" style={{ background: tile.color }} />}
      <div className="mt-[2px] flex-1 leading-[1.05]">
        <div className="truncate font-mono text-[9px] font-medium uppercase tracking-tight text-ink-primary">{tile.short}</div>
        {tile.price != null && <div className="font-mono text-[9px] text-ink-dim">{tile.price}</div>}
      </div>
      {houses > 0 && (
        <div className="absolute right-[2px] top-[8px] flex gap-[1px]">
          {Array.from({ length: houses }).map((_, i) => (
            <span key={i} className="h-[3px] w-[3px] rounded-[1px] bg-emerald-400" />
          ))}
        </div>
      )}
      {here.length > 0 && (
        <div className="absolute bottom-[2px] left-[2px] flex flex-wrap gap-[2px]">
          {here.map((t) => (
            <span
              key={t.id}
              title={t.name}
              className="h-2 w-2 rounded-full ring-1 ring-black/40"
              style={{ background: t.color }}
            />
          ))}
        </div>
      )}
      {corner && <div className="absolute inset-0 -z-10 bg-surface-lowest/60" />}
    </div>
  );
}

export function MonopolyBoard({
  teams,
  turnTeam,
  dice,
  round,
  timer,
}: {
  teams: MonoTeam[];
  turnTeam: number;
  dice: [number, number];
  round: number;
  timer: string;
}) {
  const active = teams[turnTeam];
  const onTile = active ? active.position : 0;
  return (
    <div className="mx-auto w-full max-w-[min(100%,70vh,760px)]">
      <div
        className="grid aspect-square w-full gap-[3px]"
        style={{ gridTemplateColumns: "repeat(11, 1fr)", gridTemplateRows: "repeat(11, 1fr)" }}
      >
        {MONO_BOARD.map((tile) => (
          <Tile key={tile.id} tile={tile} teams={teams} isCurrent={tile.id === onTile} />
        ))}

        {/* Center panel */}
        <div
          style={{ gridRow: "2 / 11", gridColumn: "2 / 11" }}
          className="relative flex flex-col items-center justify-center rounded-2xl border border-border-soft bg-surface-lowest/40"
        >
          <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Net Worth Leader</span>
          {(() => {
            const leader = [...teams].sort((a, b) => b.netWorth - a.netWorth)[0];
            return (
              <>
                <div className="mt-1 font-display text-2xl font-bold" style={{ color: leader?.color }}>
                  {leader?.name}
                </div>
                <div className="font-mono text-[12px] text-ink-dim">${leader?.netWorth.toLocaleString("en-US")}</div>
              </>
            );
          })()}

          <div className="mt-5 flex items-center gap-2">
            {dice.map((n, i) => (
              <span
                key={i}
                className="grid h-9 w-9 place-items-center rounded-lg border border-border-strong bg-surface text-lg font-bold text-ink-primary"
              >
                {n}
              </span>
            ))}
          </div>
          <div className="mt-3 text-center">
            <div className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">
              Round {round} · ⏱ {timer}
            </div>
            {active && (
              <div className="mt-1 inline-flex items-center gap-1.5 font-mono text-[11px]" style={{ color: active.color }}>
                <span className="h-2 w-2 rounded-full" style={{ background: active.color }} />
                {active.name} to move
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

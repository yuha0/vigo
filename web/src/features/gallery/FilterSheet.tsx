import { ChevronRight } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import type { Source, TagCount } from "@/gen/vigo/core/v1/library_pb";
import { tagGroup, tagGroups, tagLabel, type TagGroupId } from "@/features/gallery/tags";
import { cn } from "@/lib/utils";

export function FilterChip({
  label,
  count,
  active,
  onClick,
  title,
}: {
  label: string;
  count?: number;
  active: boolean;
  onClick: () => void;
  title?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      title={title}
      onClick={onClick}
      className={cn(
        "inline-flex h-10 max-w-full shrink-0 items-center gap-2 rounded-full px-4 text-sm font-medium outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50 [@media(pointer:coarse)]:h-11",
        active ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground hover:text-foreground",
      )}
    >
      <span className="truncate">{label}</span>
      {count !== undefined && <span className="tabular-nums opacity-60">{count}</span>}
    </button>
  );
}

/** Tag filter, grouped by namespace. A sheet on phones and a dialog on wider screens. */
export function FilterSheet({
  open,
  onOpenChange,
  tags,
  sources,
  selected,
  onToggle,
  onClear,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tags: TagCount[];
  sources: Source[];
  selected: string[];
  onToggle: (tag: string) => void;
  onClear: () => void;
}) {
  const byGroup = new Map<TagGroupId, TagCount[]>();
  for (const t of tags) {
    if (!t.tag) continue;
    const g = tagGroup(t.tag.name);
    byGroup.set(g, [...(byGroup.get(g) ?? []), t]);
  }

  const group = (id: TagGroupId, title: string) => {
    const items = byGroup.get(id);
    if (!items?.length) return null;
    return (
      <section key={id} aria-label={title}>
        <h3 className="mb-2 text-xs font-medium tracking-[0.14em] text-muted-foreground uppercase">{title}</h3>
        <div className="flex flex-wrap gap-2">
          {items.map((t) => (
            <FilterChip
              key={t.tag!.name}
              label={tagLabel(t.tag!.name, sources)}
              title={t.tag!.name}
              count={t.count}
              active={selected.includes(t.tag!.name)}
              onClick={() => onToggle(t.tag!.name)}
            />
          ))}
        </div>
      </section>
    );
  };

  const advanced = tagGroups.filter((g) => g.advanced && byGroup.get(g.id)?.length);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Filter</DialogTitle>
          <DialogDescription>Artworks matching every selected tag.</DialogDescription>
        </DialogHeader>
        <div className="grid max-h-[55dvh] gap-6 overflow-y-auto pr-1">
          {tagGroups.filter((g) => !g.advanced).map((g) => group(g.id, g.title))}
          {advanced.length > 0 && (
            <details className="group/adv">
              <summary className="flex min-h-11 cursor-pointer list-none items-center gap-1.5 text-sm font-medium text-muted-foreground [&::-webkit-details-marker]:hidden">
                <ChevronRight className="size-4 transition-transform group-open/adv:rotate-90" />
                Advanced
              </summary>
              <p className="mb-3 text-sm text-muted-foreground">The TV's own categories. It reports only ids, not names.</p>
              {advanced.map((g) => group(g.id, g.title))}
            </details>
          )}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={onClear} disabled={!selected.length}>
            Clear all
          </Button>
          <Button onClick={() => onOpenChange(false)}>Show results</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

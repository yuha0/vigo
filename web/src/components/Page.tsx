import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

export function Page({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("mx-auto w-full max-w-4xl px-4 pt-6 pb-16 sm:px-8 sm:pt-10 lg:px-12 lg:pt-14", className)}>{children}</div>;
}

export function PageHeader({
  eyebrow,
  title,
  description,
  actions,
}: {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="mb-7 flex flex-wrap items-end justify-between gap-x-6 gap-y-4 sm:mb-10">
      <div className="min-w-0 animate-fade-up">
        {eyebrow && <p className="mb-2 text-xs font-medium tracking-[0.14em] text-muted-foreground uppercase">{eyebrow}</p>}
        <h1 className="font-display text-[2rem] leading-[1.1] font-normal sm:text-[2.5rem]">{title}</h1>
        {description && <p className="mt-2.5 max-w-xl text-[0.9375rem] text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </header>
  );
}

export function Section({ title, description, children, id, className }: { title: ReactNode; description?: ReactNode; children: ReactNode; id?: string; className?: string }) {
  return (
    <section id={id} className={cn("scroll-mt-6 animate-fade-up", className)}>
      <div className="mb-3 px-1">
        <h2 className="font-display text-xl font-normal">{title}</h2>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {children}
    </section>
  );
}

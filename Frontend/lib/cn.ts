import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// cn merges conditional class lists and dedupes conflicting Tailwind utilities —
// the standard shadcn helper, used across the console UI kit.
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

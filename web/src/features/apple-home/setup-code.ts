/** Formats a HomeKit setup code as XXX-XX-XXX, whatever separators it arrived with. */
export function formatSetupCode(code: string): string {
  const d = code.replace(/\D/g, "");
  if (d.length !== 8) return code;
  return `${d.slice(0, 3)}-${d.slice(3, 5)}-${d.slice(5)}`;
}

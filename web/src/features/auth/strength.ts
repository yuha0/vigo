export type Strength = { score: 0 | 1 | 2 | 3 | 4; label: string };

const labels = ["Too short", "Weak", "Fair", "Good", "Strong"] as const;

/**
 * A rough, local strength hint: length matters most, variety helps, and
 * repeats or plain sequences count against it. It's guidance, not a rule.
 */
export function passwordStrength(pw: string, min: number): Strength {
  if (pw.length < min) return { score: 0, label: labels[0] };
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length;
  let points = 1;
  if (pw.length >= min + 4) points++;
  if (pw.length >= 16) points++;
  if (classes >= 3) points++;
  if (/(.)\1{2,}/.test(pw) || /^(?:0123|1234|abcd|qwer|pass)/i.test(pw)) points--;
  if (new Set(pw).size < pw.length / 3) points--;
  const score = Math.max(1, Math.min(4, points)) as Strength["score"];
  return { score, label: labels[score] };
}

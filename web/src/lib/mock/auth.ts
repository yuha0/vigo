import { Code, ConnectError } from "@connectrpc/connect";

export type AuthMode = "setup" | "login" | "ok";
export const authModes: readonly AuthMode[] = ["setup", "login", "ok"];

/** The password the mock accepts; shown in the dev-only mock switcher. */
export const DEV_PASSWORD = "framed-art-2026";
export const MIN_PASSWORD_LENGTH = 10;

/** One password and one browser session, enforced on every mock RPC. */
export class MockAuth {
  password: string | null;
  session: boolean;
  /** When set, Login answers resource_exhausted, as the real rate limiter does. */
  rateLimited = false;
  /** Lets this many wrong attempts through before rate limiting kicks in. */
  attemptsBeforeLimit = 5;
  private failures = 0;

  constructor(mode: AuthMode) {
    this.password = mode === "setup" ? null : DEV_PASSWORD;
    this.session = mode === "ok";
  }

  require() {
    if (!this.session) throw new ConnectError("sign in first", Code.Unauthenticated);
  }

  state() {
    return { passwordSet: this.password !== null, authenticated: this.session, minPasswordLength: MIN_PASSWORD_LENGTH };
  }

  private checkNew(password: string) {
    if (password.length < MIN_PASSWORD_LENGTH) {
      throw new ConnectError(`The password must be at least ${MIN_PASSWORD_LENGTH} characters.`, Code.InvalidArgument);
    }
  }

  setup(password: string) {
    if (this.password !== null) throw new ConnectError("A password is already set.", Code.FailedPrecondition);
    this.checkNew(password);
    this.password = password;
    this.session = true;
  }

  login(password: string) {
    if (this.rateLimited) throw new ConnectError("Too many attempts.", Code.ResourceExhausted);
    if (this.password === null || password !== this.password) {
      this.failures++;
      if (this.failures >= this.attemptsBeforeLimit) this.rateLimited = true;
      throw new ConnectError("Wrong password.", Code.Unauthenticated);
    }
    this.failures = 0;
    this.session = true;
  }

  logout() {
    this.session = false;
  }

  change(current: string, next: string) {
    this.require();
    if (current !== this.password) throw new ConnectError("The current password is wrong.", Code.Unauthenticated);
    this.checkNew(next);
    this.password = next;
  }
}

export interface RoomNameState {
  roomId: string;
  displayName: string;
  defaultName: string;
  customName: string | null;
  revision: number;
}

export class RoomNameError extends Error {
  constructor(message: string, public status: number | null, public current?: RoomNameState) {
    super(message);
    this.name = "RoomNameError";
  }
}

export function validateRoomName(name: string): string | null {
  if (/[\u0000-\u001f\u007f-\u009f]/u.test(name)) return "Use a name without control characters.";
  const length = [...name.trim()].length;
  return length < 1 || length > 200 ? "Use a name between 1 and 200 characters." : null;
}

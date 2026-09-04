import { toast } from "@heroui/react";

export function notifySuccess(message: string) {
  toast.success(message, { timeout: 2800 });
}

export function notifyError(message: string) {
  toast.danger(message, { timeout: 4500 });
}
